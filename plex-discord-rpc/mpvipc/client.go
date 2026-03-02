package mpvipc

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"plex-discord-rpc/mpvipc/pipe"
	"sync"
)

type Client struct {
	reqid  int
	socket net.Conn

	mutex    *sync.Mutex
	qchan    chan struct{}
	requests map[int]*request
}

func NewClient() *Client {
	client := &Client{reqid: 1}
	client.mutex = new(sync.Mutex)
	client.qchan = make(chan struct{})
	client.requests = make(map[int]*request)
	return client
}

func (c *Client) Open(path string) (err error) {
	c.socket, err = pipe.GetPipeSocket(path)
	go c.readloop()
	return
}

func (c *Client) readloop() {
	reader := bufio.NewReader(c.socket)
	for {
		select {
		case <-c.qchan:
			return
		default:
			// read data from socket
			data, err := reader.ReadBytes('\n')
			if err != nil {
				log.Println("readloop error:", err)
				c.Close()
				return
			}

			// unmarshal received data
			var res response
			err = json.Unmarshal(data, &res)
			if err != nil {
				continue
			}

			// handle response
			c.mutex.Lock()
			if req, ok := c.requests[res.RequestID]; ok {
				delete(c.requests, res.RequestID)
				req.reschan <- &res
			}
			c.mutex.Unlock()
		}
	}
}

func (c *Client) write(req *request) (*request, error) {
	defer func() {
		c.reqid += 1
	}()
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	_, err = c.socket.Write(append(data, '\n'))
	if err != nil {
		return nil, err
	}
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.requests[req.RequestID] = req
	return req, nil
}

func (c *Client) Call(cmd string, args ...interface{}) (interface{}, error) {
	req, err := c.write(newRequest(c.reqid, cmd, args...))
	if err != nil {
		return nil, err
	}
	return req.Response()
}

func (c *Client) GetProperty(key string) (interface{}, error) {
	return c.Call("get_property", key)
}

func (c *Client) GetPropertyString(key string) (string, error) {
	value, err := c.Call("get_property_string", key)
	if err != nil {
		return "", err
	}
	if value == nil {
		value = ""
	}
	return value.(string), nil
}

func (c *Client) Close() error {
    c.mutex.Lock()
	for _, req := range c.requests { close(req.reschan) }
	c.requests = make(map[int]*request)
    defer c.mutex.Unlock()

    if c.socket == nil { return nil }

    err := c.socket.Close()
    c.socket = nil
    return err
}

func (c *Client) IsClosed() bool { return c.socket == nil }
