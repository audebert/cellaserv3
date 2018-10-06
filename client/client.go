package client

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"regexp"
	"sync/atomic"
	"time"

	cellaserv "bitbucket.org/evolutek/cellaserv2-protobuf"
	"github.com/evolutek/cellaserv3/common"
	"github.com/golang/protobuf/proto"
)

type subscriberHandler func(eventName string, eventData []byte)

type subscriber struct {
	eventPattern *regexp.Regexp
	handle       subscriberHandler
}

type client struct {
	conn        net.Conn
	services    map[string]map[string]*service
	subscribers []*subscriber

	currentRequestId uint64
	requestsInFlight map[uint64]chan *cellaserv.Reply

	closed chan bool
}

func (c *client) sendRequestWaitForReply(req *cellaserv.Request) *cellaserv.Reply {
	// Add message Id
	*req.Id = atomic.AddUint64(&c.currentRequestId, 1)
	reqBytes, err := proto.Marshal(req)
	if err != nil {
		panic(fmt.Sprintf("Could not marshal request: %s", err))
	}

	if _, ok := c.requestsInFlight[*req.Id]; ok {
		panic(fmt.Sprintf("Duplicate Request Id: %d", *req.Id))
	}

	// Track request id
	c.requestsInFlight[*req.Id] = make(chan *cellaserv.Reply)

	msgType := cellaserv.Message_Request
	msg := cellaserv.Message{Type: &msgType, Content: reqBytes}

	common.SendMessage(c.conn, &msg)

	// Wait for reply
	return <-c.requestsInFlight[*req.Id]
}

// handleRequest
func (c *client) handleRequest(req *cellaserv.Request) ([]byte, error) {
	name := *req.ServiceName
	method := *req.Method
	id := *req.Id

	var ident string
	if *req.ServiceIdentification != "" {
		ident = *req.ServiceIdentification
		log.Debug("[Request] id:%d %s[%s].%s", id, name, ident, method)
	} else {
		log.Debug("[Request] id:%d %s.%s", id, name, method)
	}

	// Find service instance
	idents, ok := c.services[name]
	if !ok || len(idents) == 0 {
		panic(fmt.Sprintf("[Request] id:%d No such service: %s", id, name))
	}

	srvc, ok := idents[ident]
	return srvc.handleRequest(req, method)
}

func (c *client) handleRequestReply(req *cellaserv.Request) {
	// Handle request
	replyBytes, err := c.handleRequest(req)

	// Send reply
	msgType := cellaserv.Message_Reply
	msgContent := &cellaserv.Reply{Id: req.Id, Data: replyBytes}

	if err != nil {
		// Add error info
		errString := err.Error()
		msgContent.Error = &cellaserv.Reply_Error{
			Type: cellaserv.Reply_Error_Custom.Enum(),
			What: &errString,
		}
	}

	msgContentBytes, _ := proto.Marshal(msgContent)
	msg := &cellaserv.Message{Type: &msgType, Content: msgContentBytes}

	common.SendMessage(c.conn, msg)
}

func (c *client) handleReply(rep *cellaserv.Reply) error {
	replyChan, ok := c.requestsInFlight[rep.GetId()]
	if !ok {
		return fmt.Errorf("Could not find request matching reply: %s", rep.String())
	}
	replyChan <- rep
	return nil
}

func (c *client) handlePublish(pub *cellaserv.Publish) {
	eventName := pub.GetEvent()
	log.Info("[Publish] received: %s", eventName)
	for _, h := range c.subscribers {
		if h.eventPattern.Match([]byte(eventName)) {
			log.Debug("[Publish] %v matched %s", h, eventName)
			h.handle(eventName, pub.GetData())
		}
	}
}

func (c *client) handleMessage(msg *cellaserv.Message) error {
	var err error

	// Parse and process message payload
	switch *msg.Type {
	case cellaserv.Message_Request:
		request := &cellaserv.Request{}
		err = proto.Unmarshal(msg.Content, request)
		if err != nil {
			return fmt.Errorf("Could not unmarshal request: %s", err)
		}
		c.handleRequestReply(request)
	case cellaserv.Message_Publish:
		pub := &cellaserv.Publish{}
		err = proto.Unmarshal(msg.Content, pub)
		if err != nil {
			return fmt.Errorf("Could not unmarshal publish: %s", err)
		}
		c.handlePublish(pub)
	case cellaserv.Message_Reply:
		rep := &cellaserv.Reply{}
		err := proto.Unmarshal(msg.Content, rep)
		if err != nil {
			return fmt.Errorf("Could not unmarshal reply: %s", err)
		}
		return c.handleReply(rep)
	case cellaserv.Message_Subscribe:
		fallthrough
	case cellaserv.Message_Register:
		return fmt.Errorf("Client received unsupported message type: %d", *msg.Type)
	default:
		return fmt.Errorf("Unknown message type: %d", *msg.Type)
	}
	return nil
}

// TODO(halfr): replace by idiomatic context.Context.Done()<-true
func (c *client) Close() {
	c.closed <- true
}

// TODO(halfr) replace by idiomatic '<-c.ctx.Done()'
func (c *client) WaitClose() {
	<-c.closed
}

func (c *client) RegisterService(s *service) {
	// Make sure the second map is created
	if _, ok := c.services[s.Name]; !ok {
		c.services[s.Name] = make(map[string]*service)
	}
	// Keep a pointer to the service
	c.services[s.Name][s.Identification] = s

	// Send register message to cellaserv
	msgType := cellaserv.Message_Register
	msgContent := &cellaserv.Register{
		Name:           &s.Name,
		Identification: &s.Identification,
	}
	msgContentBytes, _ := proto.Marshal(msgContent)
	msg := &cellaserv.Message{Type: &msgType, Content: msgContentBytes}
	common.SendMessage(c.conn, msg)

	log.Info("Service %s registered", s)
}

func (c *client) Publish(event string, data interface{}) {
	log.Debug("[Publish] %s(%#v)", event, data)

	// Serialize request payload
	dataBytes, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("Could not marshal to JSON: %v", data))
	}

	// Prepare Publish message
	pub := &cellaserv.Publish{
		Event: &event,
		Data:  dataBytes,
	}
	pubBytes, err := proto.Marshal(pub)
	if err != nil {
		panic(fmt.Sprintf("Could not marshal publish: %s", err))
	}

	// Send message
	msgType := cellaserv.Message_Publish
	msg := &cellaserv.Message{Type: &msgType, Content: pubBytes}
	common.SendMessage(c.conn, msg)
}

func (c *client) Subscribe(eventPattern *regexp.Regexp, handler subscriberHandler) error {
	// Get string representing the event regexp
	eventPatternStr := eventPattern.String()

	// Create and add to subscriber map
	s := &subscriber{
		eventPattern: eventPattern,
		handle:       handler,
	}
	log.Debug("[Subscribe] Adding subsriber %p to event pattern: %s", s, eventPatternStr)
	c.subscribers = append(c.subscribers, s)

	// Prepare subscribe message
	msgType := cellaserv.Message_Subscribe
	sub := &cellaserv.Subscribe{Event: &eventPatternStr}
	subBytes, err := proto.Marshal(sub)
	if err != nil {
		return fmt.Errorf("Could not marshal subscribe: %s", err)
	}

	msg := cellaserv.Message{Type: &msgType, Content: subBytes}

	// Send subscribe message
	common.SendMessage(c.conn, &msg)

	return nil
}

// NewConnection returns a Client instance connected to cellaserv or panics
func NewConnection(address string) *client {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		panic(fmt.Errorf("Could not connect to cellaserv: %s", err))
	}

	c := &client{
		conn:             conn,
		services:         make(map[string]map[string]*service),
		requestsInFlight: make(map[uint64]chan *cellaserv.Reply),
		currentRequestId: rand.Uint64(),
		closed:           make(chan bool),
	}

	// Handle goroutine
	go func() {
		for {
			closed, _, msg, err := common.RecvMessage(conn)
			if err != nil {
				log.Error("[Message] Receive: %s", err)
			}
			if closed {
				log.Info("[Net] Connection closed")
				c.Close()
				break
			}
			err = c.handleMessage(msg)
			if err != nil {
				log.Error("[Message] Handle: %s", err)
			}
		}
	}()

	return c
}

func init() {
	rand.Seed(time.Now().UnixNano())
}
