package coap

/*
var path = "/oic/d"
var udpServer = "127.0.0.1:52593"
var tcpServer = "127.0.0.1:40993"

func decodeMsg(resp Message) {
	var m interface{}
	fmt.Printf("--------------------------------------\n")
	fmt.Printf("path: %v\n", resp.PathString())
	fmt.Printf("code: %v\n", resp.Code())
	fmt.Printf("type: %v\n", resp.Type())
	contentFormat := TextPlain
	if resp.Option(ContentFormat) != nil {
		contentFormat = resp.Option(ContentFormat).(MediaType)
		fmt.Printf("content format: %v\n", contentFormat)
	}
	if resp.Payload() != nil && len(resp.Payload()) > 0 {
		switch contentFormat {
		case AppCBOR:
			err := codec.NewDecoderBytes(resp.Payload(), new(codec.CborHandle)).Decode(&m)
			if err != nil {
				fmt.Printf("cannot decode payload: %v!!!\n", err)
			} else {
				fmt.Printf("payload type: %T\n", m)
				fmt.Printf("payload value: %v\n", m)
			}
		case AppJSON:
			err := codec.NewDecoderBytes(resp.Payload(), new(codec.JsonHandle)).Decode(&m)
			if err != nil {
				fmt.Printf("cannot decode payload: %v!!!\n", err)
			} else {
				fmt.Printf("payload type: %T\n", m)
				fmt.Printf("payload value: %v\n", m)
			}
		}
	}
	fmt.Printf("resp raw: %v\n", resp)
}

func observe(w ResponseWriter, req *Request) {
	fmt.Printf("OBSERVE : %v\n", req.Client.RemoteAddr())
	decodeMsg(req.Msg)
}

func TestBlockWisePostBlock16(t *testing.T) {
	szx := BlockWiseSzx16
	client := &Client{Net: "udp", Handler: observe, BlockWiseTransferSzx: &szx}
	co, err := client.Dial(udpServer)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}

	fmt.Printf("conn: %v\n", co.LocalAddr())

	payload := map[string]interface{}{
		"binaryAttribute": make([]byte, 33),
	}
	bw := new(bytes.Buffer)
	h := new(codec.CborHandle)
	enc := codec.NewEncoder(bw, h)
	err = enc.Encode(&payload)
	if err != nil {
		t.Fatalf("Cannot encode: %v", err)
	}

	resp, err := co.Post(path, AppCBOR, bw)
	if err != nil {
		t.Fatalf("Cannot post exchange")
	}
	decodeMsg(resp)
}

func TestBlockWiseGetBlock16(t *testing.T) {
	szx := BlockWiseSzx16
	client := &Client{Net: "udp", Handler: observe, BlockWiseTransferSzx: &szx}

	co, err := client.Dial(udpServer)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}
	req, err := co.NewGetRequest(path)
	if err != nil {
		t.Fatalf("Cannot create %v", err)
	}
	block2, err := MarshalBlockOption(BlockWiseSzx16, 0, false)
	if err != nil {
		t.Fatalf("Cannot marshal block %v", err)
	}
	req.SetOption(Block2, block2)
	decodeMsg(req)
	resp, err := co.Exchange(req)
	if err != nil {
		t.Fatalf("Cannot post exchange")
	}
	decodeMsg(resp)
}

func TestBlockWiseObserveBlock16(t *testing.T) {
	szx := BlockWiseSzx16
	sync := make(chan bool)
	client := &Client{Net: "udp", Handler: func(w ResponseWriter, req *Request) {
		observe(w, req)
		t.Fatalf("unexpected  called handler")
		sync <- true
	}, BlockWiseTransferSzx: &szx}

	co, err := client.Dial(udpServer)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}
	_, err = co.Observe(path, func(req *Request) {
		decodeMsg(req.Msg)
		sync <- true
	})
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	<-sync
	co.Close()
}

func TestBlockWiseMulticastBlock16(t *testing.T) {
	szx := BlockWiseSzx16
	client := &MulticastClient{Net: "udp", Handler: observe, BlockWiseTransferSzx: &szx}

	co, err := client.Dial("224.0.1.187:5683")
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}
	sync := make(chan bool)
	_, err = co.Publish("/oic/res", func(req *Request) {
		decodeMsg(req.Msg)
		sync <- true
	})
	if err != nil {
		t.Fatalf("Unexpected error '%v'", err)
	}
	<-sync
}

func TestGetBlock16(t *testing.T) {
	szx := BlockWiseSzx16
	bw := false
	client := &Client{Net: "tcp", Handler: observe, BlockWiseTransfer: &bw, BlockWiseTransferSzx: &szx}

	co, err := client.Dial(tcpServer)
	if err != nil {
		t.Fatalf("Error dialing: %v", err)
	}
	resp, err := co.Get("/oic/res")
	if err != nil {
		t.Fatalf("Cannot post exchange")
	}
	decodeMsg(resp)
}
*/
