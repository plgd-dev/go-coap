module github.com/plgd-dev/go-coap/v3

go 1.24.0

require (
	github.com/dsnet/golib/memfile v1.0.0
	github.com/pion/dtls/v3 v3.0.7
	github.com/stretchr/testify v1.11.1
	go.uber.org/atomic v1.11.0
	golang.org/x/exp v0.0.0-20251113190631-e25ba8c21ef6
	golang.org/x/net v0.35.0
	golang.org/x/sync v0.18.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/transport/v3 v3.0.7 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// pin versions to keep go1.20 support
replace (
	golang.org/x/crypto => golang.org/x/crypto v0.33.0
	golang.org/x/exp => golang.org/x/exp v0.0.0-20251113190631-e25ba8c21ef6
	golang.org/x/net => golang.org/x/net v0.35.0
	golang.org/x/sync => golang.org/x/sync v0.11.0
	golang.org/x/sys => golang.org/x/sys v0.30.0
)
