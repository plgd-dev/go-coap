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

	"github.com/plgd-dev/go-coap/v2/net"
	"github.com/plgd-dev/go-coap/v2/udp"
	"github.com/plgd-dev/go-coap/v2/udp/client"
	"github.com/plgd-dev/go-coap/v2/udp/message"
	"github.com/plgd-dev/go-coap/v2/udp/message/pool"
)

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to `file`")
var memprofile = flag.String("memprofile", "", "write memory profile to `file`")

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
	l, err := net.NewListenUDP("udp4", "")
	if err != nil {
		log.Println(err)
		return
	}
	defer l.Close()
	timeout := time.Second * 25
	s := udp.NewServer( /*udp.WithBlockwise(true, blockwise.SZX1024, timeout)*/ )
	defer s.Stop()
	go func() {
		err := s.Serve(l)
		if err != nil {
			log.Println(err)
		}
	}()

	d := func() {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		var numDevices uint32
		var numDuplicit uint32

		var duplicit sync.Map

		req, err := client.NewGetRequest(ctx, "/oic/res") /*msg.Option{
			ID:    msg.URIQuery,
			Value: []byte("rt=oic.wk.d"),
		}*/
		if err != nil {
			panic(fmt.Errorf("cannot create discover request: %w", err))
		}
		req.SetMessageID(message.GetMID())
		req.SetType(message.NonConfirmable)
		defer pool.ReleaseMessage(req)

		err = s.DiscoveryRequest(req, "224.0.1.187:5683", func(cc *client.ClientConn, resp *pool.Message) {
			_, loaded := duplicit.LoadOrStore(cc.RemoteAddr().String(), true)
			if loaded {
				atomic.AddUint32(&numDuplicit, 1)
			} else {
				atomic.AddUint32(&numDevices, 1)
				//log.Printf("discovered %v: %+v", cc.RemoteAddr(), resp.Message)
			}
		})

		log.Printf("Number of devices %v, Number of duplicit responses %v\n", numDevices, numDuplicit)

		if err != nil {
			log.Println(err)
		}
	}
	for {
		d()
	}
}
