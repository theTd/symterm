package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
)

type SocketClient struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	nextID uint64
}

func DialAdminSocket(path string) (*SocketClient, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	return &SocketClient{
		conn:   conn,
		reader: bufio.NewReader(conn),
		writer: bufio.NewWriter(conn),
	}, nil
}

func (c *SocketClient) Close() error {
	if c == nil || c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *SocketClient) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.nextID++
	request := RPCRequest{
		ID:     c.nextID,
		Method: method,
	}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		request.Params = raw
	}
	line, err := json.Marshal(request)
	if err != nil {
		return err
	}
	if _, err := c.writer.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := c.writer.Flush(); err != nil {
		return err
	}
	replyLine, err := c.reader.ReadBytes('\n')
	if err != nil {
		return err
	}
	var response RPCResponse
	if err := json.Unmarshal(replyLine, &response); err != nil {
		return err
	}
	if response.Error != nil {
		return errors.New(response.Error.Message)
	}
	if result == nil || len(response.Result) == 0 {
		return nil
	}
	return json.Unmarshal(response.Result, result)
}
