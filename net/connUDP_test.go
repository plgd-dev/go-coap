package net

import (
	"context"
	"fmt"
	stdnet "net"
	"sync"
	"testing"
	"time"
)

func BenchmarkUDPConnParallelWrites(b *testing.B) {
	for _, workers := range []int{1, 8, 64} {
		for _, timeout := range []bool{false, true} {
			b.Run(fmt.Sprintf("goroutines=%d/timeout=%t", workers, timeout), func(b *testing.B) {
				receiver, err := stdnet.ListenUDP("udp4", &stdnet.UDPAddr{IP: stdnet.IPv4(127, 0, 0, 1)})
				if err != nil {
					b.Fatal(err)
				}
				sender, err := NewListenUDP("udp4", "127.0.0.1:0")
				if err != nil {
					receiver.Close()
					b.Fatal(err)
				}
				readDone := make(chan struct{})
				go func() {
					defer close(readDone)
					buf := make([]byte, 2048)
					for {
						if _, _, readErr := receiver.ReadFromUDP(buf); readErr != nil {
							return
						}
					}
				}()
				defer func() {
					if err := sender.Close(); err != nil {
						b.Error(err)
					}
					if err := receiver.Close(); err != nil {
						b.Error(err)
					}
					<-readDone
				}()

				addr := receiver.LocalAddr().(*stdnet.UDPAddr)
				payload := []byte("coap benchmark payload")
				start := make(chan struct{})
				errs := make(chan error, workers)
				var wg sync.WaitGroup
				for i := 0; i < workers; i++ {
					count := b.N / workers
					if i < b.N%workers {
						count++
					}
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						for range count {
							ctx := context.Background()
							var cancel context.CancelFunc
							if timeout {
								ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
							}
							writeErr := sender.WriteWithContext(ctx, addr, payload)
							if cancel != nil {
								cancel()
							}
							if writeErr != nil {
								errs <- writeErr
								return
							}
						}
					}()
				}
				b.SetBytes(int64(len(payload)))
				b.ResetTimer()
				close(start)
				wg.Wait()
				b.StopTimer()
				close(errs)
				for writeErr := range errs {
					b.Error(writeErr)
				}
			})
		}
	}
}
