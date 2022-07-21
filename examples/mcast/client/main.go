package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"runtime/pprof"
	"sync"
	"sync/atomic"
	"time"

	"github.com/plgd-dev/go-coap/v2/message"
	"github.com/plgd-dev/go-coap/v2/message/pool"
	"github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/options"
	"github.com/plgd-dev/go-coap/v2/udp"
	"github.com/plgd-dev/go-coap/v2/udp/client"
)

// https://blog.packagecloud.io/eng/2016/06/22/monitoring-tuning-linux-networking-stack-receiving-data/#monitoring-network-data-processing
// For monitoring of dropping and interuption process packet
// cat /proc/net/softnet_stat
//   The first value, sd->processed, is the number of network frames processed. This can be more than the total number of network frames received if you are using ethernet bonding. There are cases where the ethernet bonding driver will trigger network data to be re-processed, which would increment the sd->processed count more than once for the same packet.
//   The second value, sd->dropped, is the number of network frames dropped because there was no room on the processing queue. (increasing backlog: sudo sysctl -w net.core.netdev_max_backlog=2000)
//   The third value, sd->time_squeeze, is (as we saw) the number of times the net_rx_action loop terminated because the budget was consumed or the time limit was reached, but more work could have been. Increasing the budget as explained earlier can help reduce this. (sudo sysctl -w net.core.netdev_budget=9600)
//   Others are not interesting
// Increase the maximum receive buffer size for socket by setting a sysctl. sudo sysctl -w net.core.rmem_max=8388608
// Adjust the default initial receive buffer size for socket  by setting a sysctl. sudo sysctl -w net.core.rmem_default=8388608
// Increase the maximum write buffer size for socket by setting a sysctl. sudo sysctl -w net.core.wmem_max=8388608
// Adjust the default initial receive buffer size for socket  by setting a sysctl. sudo sysctl -w net.core.wmem_default=8388608

var (
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to `file`")
	memprofile = flag.String("memprofile", "", "write memory profile to `file`")
	numDevs    = flag.Int("numdevices", 1000, "devices")
)

func main() {
	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close() // error handling omitted for example
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	// ... rest of the program ...

	if *memprofile != "" {
		f, err := os.Create(*memprofile)
		if err != nil {
			log.Fatal("could not create memory profile: ", err)
		}
		defer f.Close() // error handling omitted for example
		runtime.GC()    // get up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			log.Fatal("could not write memory profile: ", err)
		}
	}

	stable := 0
	minTimeout := time.Second * 10
	timeout := minTimeout
	messagePool := pool.New(1024, 1600)

	var previousDuplicit *sync.Map
	d := func() {
		l, err := net.NewListenUDP("udp4", "")
		if err != nil {
			log.Fatal(err)
			return
		}
		s := udp.NewServer(options.WithTransmission(time.Second, timeout/2, 2), options.WithMessagePool(messagePool))
		var wg sync.WaitGroup
		defer wg.Wait()
		defer s.Stop()
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Serve(l)
		}()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		var numDevices uint32
		var numDuplicit uint32

		var duplicit sync.Map

		token, err := message.GetToken()
		if err != nil {
			panic(fmt.Errorf("cannot get token: %w", err))
		}
		req := messagePool.AcquireMessage(ctx)
		err = req.SetupGet("/oic/res", token) /* msg.Option{
			ID:    msg.URIQuery,
			Value: []byte("rt=oic.wk.d"),
		}*/
		if err != nil {
			panic(fmt.Errorf("cannot create discover request: %w", err))
		}
		req.SetMessageID(message.GetMID())
		req.SetType(message.NonConfirmable)
		defer messagePool.ReleaseMessage(req)

		err = s.DiscoveryRequest(req, "224.0.1.187:5683", func(cc *client.ClientConn, resp *pool.Message) {
			_, loaded := duplicit.LoadOrStore(cc.RemoteAddr().String(), true)
			if loaded {
				atomic.AddUint32(&numDuplicit, 1)
			} else {
				atomic.AddUint32(&numDevices, 1)
				// log.Printf("discovered %v: %+v", cc.RemoteAddr(), resp.Message)
			}
		})

		log.Printf("Number of devices %v, Number of duplicit responses %v\n", numDevices, numDuplicit)

		previousNum := uint32(0)
		if previousDuplicit != nil {
			previousDuplicit.Range(func(key, value interface{}) bool {
				_, ok := duplicit.Load(key)
				if !ok {
					fmt.Printf("device %v is lost\n", key)
				}
				previousNum++
				return true
			})
		}

		previousDuplicit = &duplicit

		if int(numDevices) != *numDevs && previousNum != numDevices {
			timeout += time.Second
			stable = 0
			fmt.Printf("inc timeout to %v\n", timeout)
		} else {
			stable++
		}
		if stable == 10 {
			timeout -= time.Millisecond * 500
			if timeout < minTimeout {
				timeout = minTimeout
			}
			fmt.Printf("dec timeout to %v\n", timeout)
			stable = 0
		}
		if err != nil {
			log.Println(err)
		}
	}
	for {
		d()
	}
}
