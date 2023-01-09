package fn

// Slice of functions to be executed after each other
type FuncList struct {
	f []func()
}

// Add a function to be called by the Execute method.
func (c *FuncList) Add(f func()) {
	c.f = append(c.f, f)
}

// Return a function that executions all added functions
//
// Functions are executed in reverse order they were added.
func (c *FuncList) ToFunction() func() {
	return func() {
		for i := range c.f {
			c.f[len(c.f)-1-i]()
		}
	}
}

// Execute all added functions
func (c *FuncList) Execute() {
	c.ToFunction()()
}
