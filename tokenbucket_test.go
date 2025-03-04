package limiters_test

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	l "github.com/mingchouliao/limiters"
)

var tokenBucketUniformTestCases = []struct {
	capacity     int64
	refillRate   time.Duration
	requestCount int64
	requestRate  time.Duration
	missExpected int
	delta        float64
}{
	{
		5,
		time.Millisecond * 100,
		10,
		time.Millisecond * 25,
		3,
		1,
	},
	{
		10,
		time.Millisecond * 100,
		13,
		time.Millisecond * 25,
		0,
		1,
	},
	{
		10,
		time.Millisecond * 100,
		15,
		time.Millisecond * 33,
		2,
		1,
	},
	{
		10,
		time.Millisecond * 100,
		16,
		time.Millisecond * 33,
		2,
		1,
	},
	{
		10,
		time.Millisecond * 10,
		20,
		time.Millisecond * 10,
		0,
		1,
	},
	{
		1,
		time.Millisecond * 200,
		10,
		time.Millisecond * 100,
		5,
		2,
	},
}

// tokenBuckets returns all the possible TokenBucket combinations.
func (s *LimitersTestSuite) tokenBuckets(capacity int64, refillRate time.Duration, clock l.Clock) []*l.TokenBucket {
	var buckets []*l.TokenBucket
	for _, locker := range s.lockers(true) {
		for _, backend := range s.tokenBucketBackends() {
			buckets = append(buckets, l.NewTokenBucket(capacity, refillRate, locker, backend, clock, s.logger))
		}
	}
	return buckets
}

func (s *LimitersTestSuite) tokenBucketBackends() []l.TokenBucketStateBackend {
	return []l.TokenBucketStateBackend{
		l.NewTokenBucketInMemory(),
		l.NewTokenBucketEtcd(s.etcdClient, uuid.New().String(), time.Second, false),
		l.NewTokenBucketEtcd(s.etcdClient, uuid.New().String(), time.Second, true),
		l.NewTokenBucketRedis(s.redisClient, uuid.New().String(), time.Second, false),
		l.NewTokenBucketRedis(s.redisClient, uuid.New().String(), time.Second, true),
		l.NewTokenBucketDynamoDB(s.dynamodbClient, uuid.New().String(), s.dynamoDBTableProps, time.Second, false),
		l.NewTokenBucketDynamoDB(s.dynamodbClient, uuid.New().String(), s.dynamoDBTableProps, time.Second, true),
	}
}

func (s *LimitersTestSuite) TestTokenBucketRealClock() {
	clock := l.NewSystemClock()
	for _, testCase := range tokenBucketUniformTestCases {
		for _, bucket := range s.tokenBuckets(testCase.capacity, testCase.refillRate, clock) {
			wg := sync.WaitGroup{}
			// mu guards the miss variable below.
			var mu sync.Mutex
			miss := 0
			for i := int64(0); i < testCase.requestCount; i++ {
				// No pause for the first request.
				if i > 0 {
					clock.Sleep(testCase.requestRate)
				}
				wg.Add(1)
				go func(bucket *l.TokenBucket) {
					defer wg.Done()
					if _, err := bucket.Limit(context.TODO()); err != nil {
						s.Equal(l.ErrLimitExhausted, err, "%T %v", bucket, bucket)
						mu.Lock()
						miss++
						mu.Unlock()
					}
				}(bucket)
			}
			wg.Wait()
			s.InDelta(testCase.missExpected, miss, testCase.delta, testCase)
		}
	}
}

func (s *LimitersTestSuite) TestTokenBucketFakeClock() {
	for _, testCase := range tokenBucketUniformTestCases {
		clock := newFakeClock()
		for _, bucket := range s.tokenBuckets(testCase.capacity, testCase.refillRate, clock) {
			clock.reset()
			miss := 0
			for i := int64(0); i < testCase.requestCount; i++ {
				// No pause for the first request.
				if i > 0 {
					clock.Sleep(testCase.requestRate)
				}
				if _, err := bucket.Limit(context.TODO()); err != nil {
					s.Equal(l.ErrLimitExhausted, err)
					miss++
				}
			}
			s.InDelta(testCase.missExpected, miss, testCase.delta, testCase)
		}
	}
}

func (s *LimitersTestSuite) TestTokenBucketOverflow() {
	clock := newFakeClock()
	rate := time.Second
	for _, bucket := range s.tokenBuckets(2, rate, clock) {
		clock.reset()
		wait, err := bucket.Limit(context.TODO())
		s.Require().NoError(err)
		s.Equal(time.Duration(0), wait)
		wait, err = bucket.Limit(context.TODO())
		s.Require().NoError(err)
		s.Equal(time.Duration(0), wait)
		// The third call should fail.
		wait, err = bucket.Limit(context.TODO())
		s.Require().Equal(l.ErrLimitExhausted, err)
		s.Equal(rate, wait)
		clock.Sleep(wait)
		// Retry the last call.
		wait, err = bucket.Limit(context.TODO())
		s.Require().NoError(err)
		s.Equal(time.Duration(0), wait)
	}
}

func (s *LimitersTestSuite) TestTokenBucketRefill() {
	backend := l.NewTokenBucketInMemory()
	clock := newFakeClock()

	bucket := l.NewTokenBucket(4, time.Millisecond*100, l.NewLockNoop(), backend, clock, s.logger)
	sleepDurations := []int{150, 90, 50, 70}
	desiredAvailable := []int64{3, 2, 2, 2}

	_, err := bucket.Limit(context.Background())
	s.Require().NoError(err)

	_, err = backend.State(context.Background())
	s.Require().NoError(err, "unable to retrieve backend state")

	for i := range sleepDurations {
		clock.Sleep(time.Millisecond * time.Duration(sleepDurations[i]))

		_, err := bucket.Limit(context.Background())
		s.Require().NoError(err)

		state, err := backend.State(context.Background())
		s.Require().NoError(err, "unable to retrieve backend state")

		s.Require().Equal(desiredAvailable[i], state.Available)
	}
}
