package net

import (
	"context"
	"fmt"
	"io"
	stdnet "net"
	"sync"
	"testing"
	"time"
)

func BenchmarkConnParallelWrites(b *testing.B) {
	for _, workers := range []int{1, 8, 64} {
		for _, timeout := range []bool{false, true} {
			b.Run(fmt.Sprintf("goroutines=%d/timeout=%t", workers, timeout), func(b *testing.B) {
				listener, err := stdnet.Listen("tcp4", "127.0.0.1:0")
				if err != nil {
					b.Fatal(err)
				}
				defer listener.Close()
				sender, err := stdnet.Dial("tcp4", listener.Addr().String())
				if err != nil {
					b.Fatal(err)
				}
				conn := NewConn(sender)
				receiver, err := listener.Accept()
				if err != nil {
					conn.Close()
					b.Fatal(err)
				}
				readDone := make(chan error, 1)
				go func() {
					_, readErr := io.Copy(io.Discard, receiver)
					readDone <- readErr
				}()
				defer func() {
					if err := conn.Close(); err != nil {
						b.Error(err)
					}
					if err := <-readDone; err != nil {
						b.Error(err)
					}
					if err := receiver.Close(); err != nil {
						b.Error(err)
					}
				}()
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
							writeErr := conn.WriteWithContext(ctx, payload)
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
