package main

/*
 *  Copyright (C) 2015 Thomas Habets <thomas@habets.se>
 *
 *  This program is free software; you can redistribute it and/or modify
 *  it under the terms of the GNU General Public License as published by
 *  the Free Software Foundation; either version 2 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU General Public License for more details.
 *
 *  You should have received a copy of the GNU General Public License along
 *  with this program; if not, write to the Free Software Foundation, Inc.,
 *  51 Franklin Street, Fifth Floor, Boston, MA 02110-1301 USA.
 */

import (
	"testing"
)

func TestParallelSimple(t *testing.T) {
	p := &parallel{}
	var res1, res2 int
	p.add(func(ch chan<- func()) {
		defer close(ch)
		ch <- func() {
			if got, want := 0, res2; got != want {
				t.Errorf("Res2 got %d before func1, want %d", got, want)
			}
			res1 = 1
		}
	})
	p.add(func(ch chan<- func()) {
		defer close(ch)
		ch <- func() {
			if got, want := 1, res1; got != want {
				t.Errorf("Res1 got %d before func2, want %d", got, want)
			}
			res2 = 2
		}
	})
	if got, want := 0, res1; got != want {
		t.Errorf("Res1 got %d before run, want %d", got, want)
	}
	if got, want := 0, res2; got != want {
		t.Errorf("Res2 got %d before run, want %d", got, want)
	}
	p.run()
	if got, want := 1, res1; got != want {
		t.Errorf("Res1 got %d, want %d", got, want)
	}
	if got, want := 2, res2; got != want {
		t.Errorf("Res1 got %d, want %d", got, want)
	}
}

func TestParallelNil(t *testing.T) {
	p := &parallel{}
	p.add(func(ch chan<- func()) { close(ch) })
	p.run()
}

func TestParallelMulticallback(t *testing.T) {
	p := &parallel{}
	i := 0
	p.add(func(ch chan<- func()) {
		defer close(ch)
		ch <- func() {
			i += 1
		}
		ch <- func() {
			i += 2
		}
	})
	p.run()
	if got, want := i, 3; got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}
