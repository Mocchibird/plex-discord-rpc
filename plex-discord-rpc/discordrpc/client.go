package discordrpc

import ("bytes"; "encoding/binary"; "encoding/json"; "net"; "time"; "plex-discord-rpc/discordrpc/payloads"; "plex-discord-rpc/discordrpc/pipe")

type ClientError struct {
	Code    int
	Message string
}

func (err *ClientError) Error() string { return err.Message }

type Client struct {
	cid    string
	socket net.Conn
}

func NewClient(cid string) *Client {
	return &Client{cid: cid}
}

func (c *Client) read() error {
	var err error
	do := func(fn func() error) {
		if err != nil { 
			return
		}
		err = fn()
	}

	// timeout when blocking for too long
	d := time.Now().Add(time.Second * 3)
	do(func() error { return c.socket.SetReadDeadline(d) })

	// read 1024 bytes data from socket
	data := make([]byte, 1024)
	do(func() error {
		_, err2 := c.socket.Read(data)
		return err2
	})

	// parse first 8 bytes (header)
	var header struct {OPCode int32; Length int32}
	do(func() error { return binary.Read(bytes.NewReader(data[:8]), binary.LittleEndian, &header) })

	// parse remaining bytes (payload)
	var payload struct {
		// - case 1
		Code    int    `json:"code"`
		Message string `json:"message"`
		// - case 2
		Evt  string `json:"evt"`
		Data struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"data"`
	}
	do(func() error { return json.Unmarshal(bytes.Trim(data[8:], "\x00"), &payload) })

	// handle error (if any)
	if err != nil { return err}
	if (payload.Code != 0 && payload.Message != "") || payload.Evt == "ERROR" {
		return &ClientError{payload.Code, payload.Message}
	}

	return nil
}

func (c *Client) send(opcode int, payload payloads.Payload) (err error) {
	// encode data into JSON format
	data, err := json.Marshal(payload)
	if err != nil { return }

	// form the payload -> [header(opcode,length)][data]
	buffer := new(bytes.Buffer)
	_ = binary.Write(buffer, binary.LittleEndian, int32(opcode))
	_ = binary.Write(buffer, binary.LittleEndian, int32(len(data)))
	_, err = buffer.Write(data)
	if err != nil { return }

	// send out the payload
	_, err = c.socket.Write(buffer.Bytes())
	if err != nil { return }

	// wait for response and read it
	// return c.read()
	return nil // NOTE: response error is not handled
}

func (c *Client) Open() (err error) {
	if c.socket, err = pipe.GetPipeSocket(); err != nil { return }
	return c.send(0, payloads.Handshake{V: "1", ClientID: c.cid})
}

func (c *Client) Close() error {
	if c.socket == nil { return nil }
	defer func() { c.socket = nil }()
	return c.socket.Close()
}

func (c *Client) IsClosed() bool { return c.socket == nil }
