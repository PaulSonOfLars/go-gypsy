// Copyright 2013 Google, Inc.  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package yaml

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// Parse returns a root-level Node parsed from the lines read from r.  In
// general, this will be done for you by one of the File constructors.
func Parse(r io.Reader) (Node, error) {
	lb := &lineBuffer{
		Reader: bufio.NewReader(r),
	}

	return parseNode(lb, 0, nil)
}

// Supporting types and constants

const (
	typUnknown = iota
	typSequence
	typMapping
	typScalar
)

var typNames = []string{
	"Unknown", "Sequence", "Mapping", "Scalar",
}

type lineReader interface {
	Next(minIndent int) (*indentedLine, error)
}

type indentedLine struct {
	lineno int
	indent int
	line   []byte
}

func (line *indentedLine) String() string {
	return fmt.Sprintf("%2d: %s%s", line.indent,
		strings.Repeat(" ", 0*line.indent), string(line.line))
}

func parseNode(r lineReader, ind int, initial Node) (Node, error) {
	first := true
	node := initial

	// read lines
	for {
		line, err := r.Next(ind)
		if err != nil {
			return nil, err
		}
		if line == nil {
			break
		}

		if len(line.line) == 0 {
			continue
		}

		if first {
			ind = line.indent
			first = false
		}

		types := []int{}
		pieces := []string{}

		var inlineValue func([]byte) error
		inlineValue = func(partial []byte) error {
			// TODO(kevlar): This can be a for loop now
			vtyp, brk := getType(partial)
			begin, end := partial[:brk], partial[brk:]

			if vtyp == typMapping {
				end = end[1:]
			}
			end = bytes.TrimLeft(end, " ")

			switch vtyp {
			case typScalar:
				types = append(types, typScalar)
				pieces = append(pieces, string(end))
				return nil
			case typMapping:
				types = append(types, typMapping)
				pieces = append(pieces, strings.TrimSpace(string(begin)))

				trimmed := bytes.TrimSpace(end)
				if len(trimmed) == 1 && trimmed[0] == '|' {
					text := ""

					for {
						l, err := r.Next(1)
						if err != nil {
							return err
						}
						if l == nil {
							break
						}

						s := string(l.line)
						s = strings.TrimSpace(s)
						if len(s) == 0 {
							break
						}
						text = text + "\n" + s
					}

					types = append(types, typScalar)
					pieces = append(pieces, string(text))
					return nil
				}
				inlineValue(end)
			case typSequence:
				types = append(types, typSequence)
				pieces = append(pieces, "-")
				inlineValue(end)
			default:
				return fmt.Errorf("unexpected inline value type %v: %v", vtyp, typNames[vtyp])
			}
			return nil
		}

		err = inlineValue(line.line)
		if err != nil {
			return nil, err
		}
		var prev Node

		// Nest inlines
		for len(types) > 0 {
			last := len(types) - 1
			typ, piece := types[last], pieces[last]

			var current Node
			if last == 0 {
				current = node
			}
			// child := parseNode(r, line.indent+1, typUnknown) // TODO allow scalar only

			// Add to current node
			switch typ {
			case typScalar: // last will be == nil
				if _, ok := current.(Scalar); current != nil && !ok {
					return nil, fmt.Errorf("cannot append scalar to non-scalar node")
				}
				if current != nil {
					current = Scalar(piece) + " " + current.(Scalar)
					break
				}
				current = Scalar(piece)
			case typMapping:
				var mapNode Map
				var ok bool
				var child Node

				// Get the current map, if there is one
				if mapNode, ok = current.(Map); current != nil && !ok {
					_ = current.(Map) // panic
				} else if current == nil {
					mapNode = make(Map)
				}

				if _, inlineMap := prev.(Scalar); inlineMap && last > 0 {
					current = Map{
						piece: prev,
					}
					break
				}

				child, err = parseNode(r, line.indent+1, prev)
				if err != nil {
					return nil, err
				}
				mapNode[piece] = child
				current = mapNode

			case typSequence:
				var listNode List
				var ok bool
				var child Node

				// Get the current list, if there is one
				if listNode, ok = current.(List); current != nil && !ok {
					_ = current.(List) // panic
				} else if current == nil {
					listNode = make(List, 0)
				}

				if _, inlineList := prev.(Scalar); inlineList && last > 0 {
					current = List{
						prev,
					}
					break
				}

				child, err = parseNode(r, line.indent+1, prev)
				if err != nil {
					return nil, err
				}
				listNode = append(listNode, child)
				current = listNode
			default:
				return nil, fmt.Errorf("unexpected type %v: %v", typ, typNames[typ])
			}

			if last < 0 {
				last = 0
			}
			types = types[:last]
			pieces = pieces[:last]
			prev = current
		}

		node = prev
	}
	return node, nil
}

func getType(line []byte) (int, int) {
	if len(line) == 0 {
		return typUnknown, 0
	}

	if line[0] == '-' {
		return typSequence, 1
	}

	if line[0] == ' ' || line[0] == '"' {
		return typScalar, 0
	}

	// the first character is real
	// need to iterate past the first word
	// things like "foo:" and "foo :" are mappings
	// everything else is a scalar

	idx := bytes.IndexAny(line, " \":")
	if idx < 0 {
		return typScalar, 0
	}

	if line[idx] == '"' {
		return typScalar, 0
	}

	if line[idx] == ':' {
		if idx+1 < len(line) && line[idx+1] != ' ' {
			return typScalar, 0
		}
		return typMapping, idx

	} else if line[idx] == ' ' {
		// we have a space
		// need to see if its all spaces until a :
		for i := idx; i < len(line); i++ {
			switch ch := line[i]; ch {
			case ' ':
				continue
			case ':':
				// only split on colons followed by a space
				if i+1 < len(line) && line[i+1] != ' ' {
					continue
				}

				return typMapping, i
			default:
				break
			}
		}
	}

	return typScalar, 0
}

// lineReader implementations

type lineBuffer struct {
	*bufio.Reader
	readLines int
	pending   *indentedLine
}

func (lb *lineBuffer) Next(min int) (*indentedLine, error) {
	if lb.pending == nil {
		var (
			read []byte
			more bool
			err  error
		)

		l := new(indentedLine)
		l.lineno = lb.readLines
		more = true
		for more {
			read, more, err = lb.ReadLine()
			if err != nil {
				if err == io.EOF {
					return nil, nil
				}
				return nil, err
			}
			l.line = append(l.line, read...)
		}
		lb.readLines++

		for _, ch := range l.line {
			switch ch {
			case ' ':
				l.indent += 1
				continue
			default:
			}
			break
		}
		l.line = l.line[l.indent:]

		// Ignore blank lines and comments.
		if len(l.line) == 0 || l.line[0] == '#' {
			return lb.Next(min)
		}

		lb.pending = l
	}
	next := lb.pending
	if next.indent < min {
		return nil, nil // note: should be error?
	}
	lb.pending = nil
	return next, nil
}

type lineSlice []*indentedLine

func (ls *lineSlice) Next(min int) *indentedLine {
	if len(*ls) == 0 {
		return nil
	}
	next := (*ls)[0]
	if next.indent < min {
		return nil
	}
	*ls = (*ls)[1:]
	return next
}

func (ls *lineSlice) Push(line *indentedLine) {
	*ls = append(*ls, line)
}
