package oscore

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/plgd-dev/go-coap/v3/message"
)

// ProxyTarget is a CoAP origin extracted from a Proxy-Uri. Path and Query
// contain decoded URI components and are populated when present.
type ProxyTarget struct {
	Scheme string
	Host   string
	Port   string
	Path   []string
	Query  []string
}

// ParseProxyURI parses a coap or coaps Proxy-Uri and decomposes its path and
// query into the components used by OSCORE and CoAP proxy processing.
func ParseProxyURI(value string) (ProxyTarget, error) {
	uri, err := url.Parse(value)
	if err != nil {
		return ProxyTarget{}, fmt.Errorf("%w: invalid Proxy-Uri: %w", ErrInvalidOSCOREMessage, err)
	}
	if strings.Contains(value, "#") {
		return ProxyTarget{}, fmt.Errorf("%w: Proxy-Uri must not include a fragment", ErrInvalidOSCOREMessage)
	}
	target, err := parseProxyAuthority(uri)
	if err != nil {
		return ProxyTarget{}, err
	}
	target.Path, target.Query, err = parseProxyPath(uri)
	if err != nil {
		return ProxyTarget{}, err
	}
	return target, nil
}

func parseProxyAuthority(uri *url.URL) (ProxyTarget, error) {
	scheme := strings.ToLower(uri.Scheme)
	if (scheme != "coap" && scheme != "coaps") || uri.Opaque != "" || uri.Host == "" ||
		uri.User != nil || uri.Fragment != "" {
		return ProxyTarget{}, fmt.Errorf("%w: Proxy-Uri must be an absolute coap or coaps URI without userinfo or fragment", ErrInvalidOSCOREMessage)
	}
	host := strings.ToLower(uri.Hostname())
	if host == "" {
		return ProxyTarget{}, fmt.Errorf("%w: Proxy-Uri has an empty host", ErrInvalidOSCOREMessage)
	}
	port := uri.Port()
	if port == "" && strings.HasSuffix(uri.Host, ":") {
		return ProxyTarget{}, fmt.Errorf("%w: invalid Proxy-Uri port", ErrInvalidOSCOREMessage)
	}
	if port == "" {
		port = defaultProxyPort(scheme)
	} else {
		portNumber, parseErr := strconv.Atoi(port)
		if parseErr != nil || portNumber < 1 || portNumber > 65535 {
			return ProxyTarget{}, fmt.Errorf("%w: invalid Proxy-Uri port", ErrInvalidOSCOREMessage)
		}
		port = strconv.Itoa(portNumber)
	}
	return ProxyTarget{Scheme: scheme, Host: host, Port: port}, nil
}

func parseProxyPath(uri *url.URL) ([]string, []string, error) {
	escapedPath := uri.EscapedPath()
	if escapedPath != "" && escapedPath != "/" {
		segments := strings.Split(strings.TrimPrefix(escapedPath, "/"), "/")
		path := make([]string, 0, len(segments))
		for _, segment := range segments {
			decoded, decodeErr := url.PathUnescape(segment)
			if decodeErr != nil || len(decoded) > 255 {
				return nil, nil, fmt.Errorf("%w: invalid Proxy-Uri path segment", ErrInvalidOSCOREMessage)
			}
			path = append(path, decoded)
		}
		query, err := parseProxyQuery(uri)
		if err != nil {
			return nil, nil, err
		}
		return path, query, nil
	}
	query, err := parseProxyQuery(uri)
	return nil, query, err
}

func parseProxyQuery(uri *url.URL) ([]string, error) {
	if uri.RawQuery != "" {
		queries := strings.Split(uri.RawQuery, "&")
		decodedQueries := make([]string, 0, len(queries))
		for _, query := range queries {
			decoded, decodeErr := url.PathUnescape(query)
			if decodeErr != nil || len(decoded) > 255 {
				return nil, fmt.Errorf("%w: invalid Proxy-Uri query component", ErrInvalidOSCOREMessage)
			}
			decodedQueries = append(decodedQueries, decoded)
		}
		return decodedQueries, nil
	} else if uri.ForceQuery {
		return []string{""}, nil
	}
	return nil, nil
}

// AuthorityURI returns the Proxy-Uri authority form required by RFC 8613.
func (t ProxyTarget) AuthorityURI() string {
	host := t.Host
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if t.Port != defaultProxyPort(t.Scheme) {
		host = net.JoinHostPort(t.Host, t.Port)
	}
	return t.Scheme + "://" + host
}

// Address returns a host:port suitable for dialing the target.
func (t ProxyTarget) Address() string {
	return net.JoinHostPort(t.Host, t.Port)
}

func defaultProxyPort(scheme string) string {
	if scheme == "coaps" {
		return "5684"
	}
	return "5683"
}

func normalizeProxyURI(options message.Options) (message.Options, error) {
	start, end, err := options.Find(message.ProxyURI)
	if err != nil {
		if errors.Is(err, message.ErrOptionNotFound) {
			return options, nil
		}
		return nil, fmt.Errorf("find Proxy-Uri option: %w", err)
	}
	if end-start != 1 {
		return nil, fmt.Errorf("%w: duplicate Proxy-Uri option", ErrInvalidOSCOREMessage)
	}
	for _, option := range options {
		switch option.ID {
		case message.URIHost, message.URIPort, message.URIPath, message.URIQuery, message.ProxyScheme:
			return nil, fmt.Errorf("%w: Proxy-Uri cannot be combined with Uri-* or Proxy-Scheme options", ErrInvalidOSCOREMessage)
		}
	}
	target, err := ParseProxyURI(string(options[start].Value))
	if err != nil {
		return nil, err
	}
	normalized := make(message.Options, 0, len(options)+len(target.Path)+len(target.Query))
	for _, option := range options {
		if option.ID != message.ProxyURI {
			normalized = append(normalized, message.Option{ID: option.ID, Value: append([]byte(nil), option.Value...)})
		}
	}
	normalized = append(normalized, message.Option{
		ID:    message.ProxyURI,
		Value: []byte(target.AuthorityURI()),
	})
	for _, segment := range target.Path {
		normalized = append(normalized, message.Option{ID: message.URIPath, Value: []byte(segment)})
	}
	for _, query := range target.Query {
		normalized = append(normalized, message.Option{ID: message.URIQuery, Value: []byte(query)})
	}
	sortOptions(normalized)
	return normalized, nil
}

func sortOptions(options message.Options) {
	for i := 1; i < len(options); i++ {
		for j := i; j > 0 && options[j].ID < options[j-1].ID; j-- {
			options[j], options[j-1] = options[j-1], options[j]
		}
	}
}
