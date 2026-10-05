package ticket

import (
	"errors"
	"sync"
	"testing"
)

func TestConcurrentPlatformReplacementIsSafe(t *testing.T) {
	service := NewService(nil, nil, nil, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				service.SetPlatform(NewNoopPlatform(nil))
				_, _ = service.platformOrErr()
				service.SetPlatform(nil)
			}
		}()
	}
	wg.Wait()
	service.SetPlatform(nil)
	if _, err := service.platformOrErr(); !errors.Is(err, ErrNoPlatform) {
		t.Fatalf("离线平台应报错: %v", err)
	}
}
