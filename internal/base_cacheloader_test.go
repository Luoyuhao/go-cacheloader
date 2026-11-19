package internal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Luoyuhao/go-cacheloader/v2"
	"github.com/Luoyuhao/go-cacheloader/v2/memory"
	"github.com/stretchr/testify/require"
)

func TestMGetSingleKeyFallbackIsolatesSharedResults(t *testing.T) {
	for _, test := range []struct {
		name string
		keys []string
	}{
		{"success before failure", []string{"good", "bad"}},
		{"success after failure", []string{"bad", "good"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cache, err := memory.New(10)
			require.NoError(t, err)
			badStarted := make(chan struct{})
			releaseBad := make(chan struct{})
			sourceErr := errors.New("bad key source error")
			reader, err := NewReader(cache, func(ctx context.Context, key string) (int, bool, error) {
				if key == "good" {
					return 9, true, nil
				}
				close(badStarted)
				select {
				case <-releaseBad:
					return 0, false, sourceErr
				case <-ctx.Done():
					return 0, false, ctx.Err()
				}
			}, cacheloader.Config{
				LoadTimeout: 5 * time.Second,
				OnError:     func(context.Context, error) {},
			}, nil)
			require.NoError(t, err)
			defer reader.Close()

			type batchResult struct {
				values []cacheloader.Result[int]
				err    error
			}
			batchDone := make(chan batchResult, 1)
			go func() {
				values, err := reader.MGet(ctx, test.keys...)
				batchDone <- batchResult{values, err}
			}()
			select {
			case <-badStarted:
			case <-ctx.Done():
				t.Fatal("batch did not start the failing key")
			}

			// Join the in-flight key at the same point as a Get after a miss.
			waiter, joinErr := reader.start(ctx, "good", nil)
			close(releaseBad)
			require.NoError(t, joinErr)
			select {
			case <-waiter.done:
			case <-ctx.Done():
				t.Fatal("shared key result was not delivered")
			}
			require.NoError(t, waiter.err)
			entry, err := decode[int](waiter.raw)
			require.NoError(t, err)
			require.True(t, entry.Found)
			require.Equal(t, 9, entry.Value)

			batch := <-batchDone
			require.ErrorIs(t, batch.err, sourceErr)
			require.Nil(t, batch.values)
			raw, err := cache.Get(ctx, "good")
			require.NoError(t, err)
			require.Equal(t, waiter.raw, raw)
			_, err = cache.Get(ctx, "bad")
			require.ErrorIs(t, err, cacheloader.ErrNotFound)
		})
	}
}
