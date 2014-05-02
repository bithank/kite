package kite

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dgrijalva/jwt-go"
	"github.com/koding/kite/dnode"
	"github.com/koding/kite/kitekey"
)

// Request contains information about the incoming request.
type Request struct {
	Method         string
	Args           *dnode.Partial
	LocalKite      *Kite
	Client         *Client
	Username       string
	Authentication *Authentication
}

// Response is the type of the object that is returned from request handlers
// and the type of only argument that is passed to callback functions.
type Response struct {
	Error  *Error      `json:"error" dnode:"-"`
	Result interface{} `json:"result"`
}

// HandlerFunc is the type of the handlers registered to Kite.
// The returned result must be Marshalable with json package.
type HandlerFunc func(*Request) (result interface{}, err error)

// runMethod is called when a method is received from remote Kite.
func (c *Client) runMethod(method string, handlerFunc HandlerFunc, args *dnode.Partial) {
	var (
		// The request that will be constructed from incoming dnode message.
		request *Request

		// First value to the response.
		result interface{}

		// Second value to the response.
		kiteErr *Error

		// Will send the response when called.
		callback dnode.Function
	)

	// Send result if "responseCallback" exists in the request.
	defer func() {
		if callback.Caller == nil {
			return
		}

		// Only argument to the callback.
		response := Response{
			Result: result,
			Error:  kiteErr,
		}

		// Call response callback function.
		if err := callback.Call(response); err != nil {
			c.LocalKite.Log.Error(err.Error())
		}
	}()

	// Recover dnode argument errors. The caller can use functions like
	// MustString(), MustSlice()... without the fear of panic.
	defer c.LocalKite.recoverError(&kiteErr)()

	request, callback = c.newRequest(method, args)

	if !c.LocalKite.Config.DisableAuthentication {
		kiteErr = request.authenticate()
		if kiteErr != nil {
			return
		}
	}

	// Call the handler function.
	var err error
	result, err = handlerFunc(request)

	if err != nil {
		panic(err) // This will be recoverd from kite.recoverError() above.
	}
}

// HandleFunc registers a handler to run when a method call is received from a Kite.
func (k *Kite) HandleFunc(method string, handler HandlerFunc) {
	k.handlers[method] = handler
}

// runCallback is called when a callback method call is received from remote Kite.
func (c *Client) runCallback(callback func(*dnode.Partial), args *dnode.Partial) {
	kiteErr := new(Error)                      // Not used. For recovering the error.
	defer c.LocalKite.recoverError(&kiteErr)() // Do not panic no matter what.

	// Call the callback function.
	callback(args)
}

// newRequest returns a new *Request from the method and arguments passed.
func (c *Client) newRequest(method string, arguments *dnode.Partial) (request *Request, responseCallback dnode.Function) {
	// Parse dnode method arguments: [options]
	var options callOptions
	arguments.One().MustUnmarshal(&options)

	// Notify the handlers registered with Kite.OnFirstRequest().
	if c.RemoteAddr() != "" {
		c.firstRequestHandlersNotified.Do(func() {
			c.Kite = options.Kite
			c.LocalKite.callOnFirstRequestHandlers(c)
		})
	}

	request = &Request{
		Method:         method,
		Args:           options.WithArgs,
		LocalKite:      c.LocalKite,
		Client:         c,
		Authentication: options.Authentication,
	}

	return request, options.ResponseCallback
}

// authenticate tries to authenticate the user by selecting appropriate
// authenticator function.
func (r *Request) authenticate() *Error {
	// Trust the Kite if we have initiated the connection.
	// RemoteAddr() returns "" if this is an outgoing connection.
	if r.Client.RemoteAddr() == "" {
		return nil
	}

	if r.Authentication == nil {
		return &Error{
			Type:    "authenticationError",
			Message: "No authentication information is provided",
		}
	}

	// Select authenticator function.
	f := r.LocalKite.Authenticators[r.Authentication.Type]
	if f == nil {
		return &Error{
			Type:    "authenticationError",
			Message: fmt.Sprintf("Unknown authentication type: %s", r.Authentication.Type),
		}
	}

	// Call authenticator function. It sets the Request.Username field.
	err := f(r)
	if err != nil {
		return &Error{
			Type:    "authenticationError",
			Message: err.Error(),
		}
	}

	// Fix username of the remote Kite if it is invalid.
	// This prevents a Kite to impersonate someone else's Kite.
	r.Client.Kite.Username = r.Username

	return nil
}

// AuthenticateFromToken is the default Authenticator for Kite.
func (k *Kite) AuthenticateFromToken(r *Request) error {
	token, err := jwt.Parse(r.Authentication.Key, r.LocalKite.RSAKey)
	if err != nil {
		return err
	}

	if !token.Valid {
		return errors.New("Invalid signature in token")
	}

	if audience, ok := token.Claims["aud"].(string); !ok || !strings.HasPrefix(k.Kite().String(), audience) {
		return fmt.Errorf("Invalid audience in token: %s", audience)
	}

	// We don't check for exp and nbf claims here because jwt-go package already checks them.

	if username, ok := token.Claims["sub"].(string); !ok {
		return errors.New("Username is not present in token")
	} else {
		r.Username = username
	}

	return nil
}

// AuthenticateFromKiteKey authenticates user from kite key.
func (k *Kite) AuthenticateFromKiteKey(r *Request) error {
	token, err := jwt.Parse(r.Authentication.Key, kitekey.GetKontrolKey)
	if err != nil {
		return err
	}

	if !token.Valid {
		return errors.New("Invalid signature in token")
	}

	if username, ok := token.Claims["sub"].(string); !ok {
		return errors.New("Username is not present in token")
	} else {
		r.Username = username
	}

	return nil
}
