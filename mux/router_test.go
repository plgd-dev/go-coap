package mux_test

import (
	"testing"

	"github.com/plgd-dev/go-coap/v2/mux"
)

type routeTest struct {
	title        string // title of the test
	path         string
	pathTemplate string
	vars         map[string]string // the expected vars of the match
	shouldMatch  bool              // whether the request is expected to match the route at all
	pathRegexp   string
}

func TestMux(t *testing.T) {
	r := mux.NewRouter()
	tests := []routeTest{
		{
			title:        "Path route match",
			path:         "/111/222/333",
			pathTemplate: "/111/222/333",
			shouldMatch:  true,
			vars:         map[string]string{},
		},
		{
			title:        "Path route with pattern no constraints, match",
			path:         "/111/222/333",
			pathTemplate: "/111/{v1}/333",
			shouldMatch:  true,
			vars:         map[string]string{"v1": "222"},
		},
		{
			title:        "Path route with pattern, match",
			path:         "/111/222/333",
			pathTemplate: "/111/{v1:[0-9]{3}}/333",
			shouldMatch:  true,
			vars:         map[string]string{"v1": "222"},
		},
		{
			title:        "Path route with pattern, no match",
			path:         "/111/aaa/333",
			pathTemplate: "/111/{v1:[0-9]{3}}/333",
			shouldMatch:  false,
			vars:         map[string]string{"v1": "222"},
			pathRegexp:   `^/111/(?P<v0>[0-9]{3})/333$`,
		},
		{
			title:        "Path route with multiple patterns, match",
			vars:         map[string]string{"test": "111", "v2": "222", "v3": "333"},
			path:         "/111/222/333",
			pathTemplate: `/{test:[0-9]{3}}/{v2:[0-9]{3}}/{v3:[0-9]{3}}`,
			shouldMatch:  true,
		},
		{
			title:        "Path route with multiple hyphenated names and patterns with pipe, match",
			vars:         map[string]string{"product-category": "a", "product-name": "product_name", "product-id": "1"},
			path:         "/a/product_name/1",
			pathTemplate: `/{product-category:a|(?:b/c)}/{product-name}/{product-id:[0-9]+}`,
			pathRegexp:   `^/(?P<v0>a|(?:b/c))/(?P<v1>[^/]+)/(?P<v2>[0-9]+)$`,
			shouldMatch:  true,
		},
		{
			title:        "Path route with empty match right after other match",
			vars:         map[string]string{"v1": "111", "v2": "", "v3": "222"},
			path:         "/111/222",
			pathTemplate: `/{v1:[0-9]*}{v2:[a-z]*}/{v3:[0-9]*}`,
			pathRegexp:   `^/(?P<v0>[0-9]*)(?P<v1>[a-z]*)/(?P<v2>[0-9]*)$`,
			shouldMatch:  true,
		},
		{
			title:        "Path route with single pattern with pipe, match",
			vars:         map[string]string{"category": "a"},
			path:         "/a",
			pathTemplate: `/{category:a|b/c}`,
			shouldMatch:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.title, func(t *testing.T) {
			r.HandleFunc(test.pathTemplate, func(w mux.ResponseWriter, r *mux.Message) {})
			testRegexp(t, r, test)
			testRoute(t, r, test)
			r.HandleRemove(test.pathTemplate)
		})
	}
}

func testRegexp(t *testing.T, router *mux.Router, test routeTest) {

	route := router.GetRoute(test.pathTemplate)

	if route == nil {
		t.Errorf("(%v) GetRoute: expected to find route %v", test.title, test.pathTemplate)
		return
	}
	routePathRegexp, regexpErr := route.GetRouteRegexp()
	if test.pathRegexp != "" && regexpErr == nil && routePathRegexp != test.pathRegexp {
		t.Errorf("(%v) PathRegexp not equal: expected %v, got %v", test.title, test.pathRegexp, routePathRegexp)
	}
}

func testRoute(t *testing.T, router *mux.Router, test routeTest) {
	routeParams := new(mux.RouteParams)
	vars := test.vars
	route, _ := router.Match(test.path, routeParams)
	matched := route != nil
	if matched != test.shouldMatch {
		msg := "Should match"
		if !test.shouldMatch {
			msg = "Should not match"
		}
		t.Errorf("(%v) %v:\nPath: %#v\nPathTemplate: %#v\nVars: %v\n", test.title, msg, test.path, test.pathTemplate, test.vars)
	}
	if test.shouldMatch {
		if vars != nil && !stringMapEqual(vars, routeParams.Vars) {
			t.Errorf("(%v) Vars not equal: expected %v, got %v", test.title, vars, routeParams.Vars)
			return
		}
	}

}

// stringMapEqual checks the equality of two string maps
func stringMapEqual(m1, m2 map[string]string) bool {
	nil1 := m1 == nil
	nil2 := m2 == nil
	if nil1 != nil2 || len(m1) != len(m2) {
		return false
	}
	for k, v := range m1 {
		if v != m2[k] {
			return false
		}
	}
	return true
}
