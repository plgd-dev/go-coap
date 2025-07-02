module github.com/plgd-dev/go-coap/v3

go 1.23.0

toolchain go1.24.4

require (
	github.com/dsnet/golib/memfile v1.0.0
	github.com/pion/dtls/v3 v3.0.6
	github.com/stretchr/testify v1.9.0
	go.uber.org/atomic v1.11.0
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b
	golang.org/x/net v0.35.0
	golang.org/x/sync v0.15.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pion/logging v0.2.3 // indirect
	github.com/pion/transport/v3 v3.0.7 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/crypto v0.33.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

// pin versions to keep go1.20 support
replace (
	golang.org/x/crypto => golang.org/x/crypto v0.33.0
	golang.org/x/exp => golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b
	golang.org/x/net => golang.org/x/net v0.35.0
	golang.org/x/sync => golang.org/x/sync v0.11.0
	golang.org/x/sys => golang.org/x/sys v0.30.0
)
