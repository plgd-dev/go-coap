package client

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/message/pool"
	limitparallelrequests "github.com/plgd-dev/go-coap/v3/net/client/limitParallelRequests"
	"github.com/stretchr/testify/require"
)

type rfc8132TestConn struct {
	messages *pool.Pool
	released int
}

func newRFC8132TestConn() *rfc8132TestConn {
	return &rfc8132TestConn{messages: pool.New(8, 2048)}
}

func (c *rfc8132TestConn) AcquireMessage(ctx context.Context) *pool.Message {
	return c.messages.AcquireMessage(ctx)
}

func (c *rfc8132TestConn) ReleaseMessage(msg *pool.Message) {
	c.released++
	c.messages.ReleaseMessage(msg)
}

func (*rfc8132TestConn) WriteMessage(*pool.Message) error { return nil }
func (*rfc8132TestConn) AsyncPing(func()) (func(), error) { return func() {}, nil }
func (*rfc8132TestConn) Context() context.Context         { return context.Background() }

func TestRFC8132RequestBuilders(t *testing.T) {
	type builder func(*Client[*rfc8132TestConn], context.Context, string) (*pool.Message, error)
	tests := []struct {
		name  string
		code  codes.Code
		build builder
	}{
		{
			name: "FETCH",
			code: codes.FETCH,
			build: func(c *Client[*rfc8132TestConn], ctx context.Context, path string) (*pool.Message, error) {
				return c.NewFetchRequest(ctx, path, message.AppOctets, nil)
			},
		},
		{
			name: "PATCH",
			code: codes.PATCH,
			build: func(c *Client[*rfc8132TestConn], ctx context.Context, path string) (*pool.Message, error) {
				return c.NewPatchRequest(ctx, path, message.AppOctets, nil)
			},
		},
		{
			name: "iPATCH",
			code: codes.IPATCH,
			build: func(c *Client[*rfc8132TestConn], ctx context.Context, path string) (*pool.Message, error) {
				return c.NewIPatchRequest(ctx, path, message.AppOctets, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newRFC8132TestConn()
			c := New[*rfc8132TestConn](conn, nil, func() (message.Token, error) {
				return message.Token{1}, nil
			}, nil)
			ctx := context.Background()

			req, err := tt.build(c, ctx, "/resource")
			require.NoError(t, err)
			require.Equal(t, tt.code, req.Code())
			require.Equal(t, message.Token{1}, req.Token())
			path, err := req.Path()
			require.NoError(t, err)
			require.Equal(t, "/resource", path)
			conn.ReleaseMessage(req)

			tokenErr := errors.New("token generation failed")
			c.getToken = func() (message.Token, error) { return nil, tokenErr }
			_, err = tt.build(c, ctx, "/resource")
			require.ErrorIs(t, err, tokenErr)

			c.getToken = func() (message.Token, error) { return message.Token{1}, nil }
			_, err = tt.build(c, ctx, strings.Repeat("x", 256))
			require.Error(t, err)
			require.Equal(t, 3, conn.released)
		})
	}
}

func TestRFC8132ClientMethods(t *testing.T) {
	type method func(*Client[*rfc8132TestConn], context.Context) (*pool.Message, error)
	tests := []struct {
		name string
		code codes.Code
		call method
	}{
		{
			name: "FETCH",
			code: codes.FETCH,
			call: func(c *Client[*rfc8132TestConn], ctx context.Context) (*pool.Message, error) {
				return c.Fetch(ctx, "/resource", message.AppOctets, nil)
			},
		},
		{
			name: "PATCH",
			code: codes.PATCH,
			call: func(c *Client[*rfc8132TestConn], ctx context.Context) (*pool.Message, error) {
				return c.Patch(ctx, "/resource", message.AppOctets, nil)
			},
		},
		{
			name: "iPATCH",
			code: codes.IPATCH,
			call: func(c *Client[*rfc8132TestConn], ctx context.Context) (*pool.Message, error) {
				return c.IPatch(ctx, "/resource", message.AppOctets, nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := newRFC8132TestConn()
			do := func(req *pool.Message) (*pool.Message, error) {
				require.Equal(t, tt.code, req.Code())
				resp := pool.NewMessage(context.Background())
				resp.SetCode(codes.Changed)
				return resp, nil
			}
			limits := limitparallelrequests.New(0, 0, do, nil)
			c := New[*rfc8132TestConn](conn, nil, func() (message.Token, error) {
				return message.Token{1}, nil
			}, limits)

			resp, err := tt.call(c, context.Background())
			require.NoError(t, err)
			require.Equal(t, codes.Changed, resp.Code())
			conn.messages.ReleaseMessage(resp)
			require.Equal(t, 1, conn.released)

			tokenErr := errors.New("token generation failed")
			c.getToken = func() (message.Token, error) { return nil, tokenErr }
			resp, err = tt.call(c, context.Background())
			require.Nil(t, resp)
			require.ErrorIs(t, err, tokenErr)
			require.Contains(t, err.Error(), "cannot create")
			require.Equal(t, 2, conn.released)
		})
	}
}
