// Copyright Thomas Habets <thomas@habets.se> 2015
package main

// parallel runs several functions in parallel, functions that then
// write result-functions to a channel. parallel then takes care to
// run these result-functions in add order.
type parallel struct {
	chans []<-chan func()
}

func (p *parallel) add(f func(chan<- func())) {
	c := make(chan func())
	go f(c)
	p.chans = append(p.chans, c)
}

func (p *parallel) run() {
	for _, ch := range p.chans {
		f := <-ch
		if f != nil {
			f()
		}
	}
}
