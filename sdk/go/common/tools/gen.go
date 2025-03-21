// Copyright 2016-2018, Pulumi Corporation.
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

package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"os"

	"github.com/pulumi/pulumi/sdk/v3/go/common/util/contract"
)

// GenWriter adds some convenient helpers atop a buffered writer.
type GenWriter struct {
	tool string        // the name of the code-generator.
	f    *os.File      // the file being written to.
	buff *bytes.Buffer // the buffer (if there is no file).
	w    *bufio.Writer // the buffered writer used to emit code.
}

func NewGenWriter(tool string, file string) (*GenWriter, error) {
	// If the file is non-empty, open up a writer and overwrite whatever file contents already exist.
	if file != "" {
		f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		return &GenWriter{tool: tool, f: f, w: bufio.NewWriter(f)}, nil
	}

	// Otherwise, we are emitting into an in-memory buffer.
	var buff bytes.Buffer
	return &GenWriter{tool: tool, buff: &buff, w: bufio.NewWriter(&buff)}, nil
}

// Flush explicitly flushes the writer's pending writes.
func (g *GenWriter) Flush() error {
	return g.w.Flush()
}

// Close flushes and closes the underlying writer.
func (g *GenWriter) Close() error {
	err := g.w.Flush()
	contract.IgnoreError(err)
	if g.f != nil {
		return g.f.Close()
	}
	return nil
}

// WriteString writes the provided string to the underlying buffer _without_ formatting it.
func (g *GenWriter) WriteString(msg string) {
	_, err := g.w.WriteString(msg)
	contract.IgnoreError(err)
}

// Writefmt wraps the bufio.Writer.WriteString function, but also performs fmt.Sprintf-style formatting.
func (g *GenWriter) Writefmt(msg string, args ...interface{}) {
	g.WriteString(fmt.Sprintf(msg, args...))
}

// Writefmtln wraps the bufio.Writer.WriteString function, performing fmt.Sprintf-style formatting and appending \n.
func (g *GenWriter) Writefmtln(msg string, args ...interface{}) {
	g.Writefmt(msg+"\n", args...)
}

// EmitHeaderWarning emits the standard "WARNING" into a generated file, prefixed by commentChars.
func (g *GenWriter) EmitHeaderWarning(commentChars string) {
	g.Writefmtln("%s *** WARNING: this file was generated by %v. ***", commentChars, g.tool)
	g.Writefmtln("%s *** Do not edit by hand unless you're certain you know what you are doing! ***", commentChars)
	g.Writefmtln("")
}

// Buffer returns whatever has been written to the in-memory buffer (in non-file cases).
func (g *GenWriter) Buffer() string {
	return g.buff.String()
}
