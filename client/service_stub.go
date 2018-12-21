package client

import (
	"encoding/json"
	"fmt"

	cellaserv "bitbucket.org/evolutek/cellaserv2-protobuf"
)

type serviceStub struct {
	name           string
	identification string

	client *Client
}

func (s *serviceStub) String() string {
	if s.identification == "" {
		return s.name
	}
	return fmt.Sprintf("%s[%s]", s.name, s.identification)
}

func (s *serviceStub) Request(method string, data interface{}) ([]byte, error) {
	s.client.logger.Debugf("[Request] %s.%s(%#v)", s, method, data)

	// Serialize request payload
	dataBytes, err := json.Marshal(data)
	if err != nil {
		panic(fmt.Sprintf("Could not marshal to JSON: %v", data))
	}

	// Create Request
	req := &cellaserv.Request{
		Data:                  dataBytes,
		ServiceName:           s.name,
		ServiceIdentification: s.identification,
		Method:                method,
		// Id set by client
	}

	reply := s.client.sendRequestWaitForReply(req)

	// Check for errors
	replyError := reply.GetError()
	if replyError != nil {
		s.client.logger.Errorf("[Reply] Error: %s", replyError.String())
		return nil, fmt.Errorf(replyError.String())
	}

	return reply.GetData(), nil
}

func NewServiceStub(c *Client, name string, identification string) *serviceStub {
	return &serviceStub{
		name:           name,
		identification: identification,
		client:         c,
	}
}
