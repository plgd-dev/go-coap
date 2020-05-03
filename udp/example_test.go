package udp_test

func ExampleGet() {
	conn, err := udp.Dial("pluggedin.cloud:5683")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()
	res, err := conn.Get("/oic/res")
	if err != nil {
		log.Fatal(err)
	}
	data, err := ioutil.ReadAll(res.Payload())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%v", data)
}

func ExampleServe() {
	l, err := coapNet.ListenUDP("udp", "0.0.0.0:5683")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	s := NewServer()
	defer s.Stop()
	log.Fatal(s.Serve(l))
}


func ExampleDiscovery() {
	l, err := coapNet.ListenUDP("udp", "")
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()
	var wg sync.WaitGroup
	defer wg.Wait()

	s := NewServer()
	defer s.Stop()
	wg.Add(1)
	go func() {
		defer wg.Done()
		err := s.Serve(l)
		log.Println(err)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := s.Discover(ctx, "224.0.1.187:5683", "/oic/res", func(cc *ClientConn, res *Message){
		data, err := ioutil.ReadAll(res.Payload())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%v", data)
	})
	if err != nil {
		log.Fatal(err)
	}
}