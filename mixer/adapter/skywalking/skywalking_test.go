// Copyright 2017 Istio Authors.
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

package skywalking

import (
	"testing"
	"reflect"
	"istio.io/istio/mixer/template/metric"
)

type testServer struct {
	errOnStart bool
}

func TestGetInfo(t *testing.T) {
	i := GetInfo()
	if i.Name != "skywalking" {
		t.Fatalf("GetInfo().Name=%s; want %s", i.Name, "skywalking")
	}

	if !reflect.DeepEqual(i.SupportedTemplates, []string{metric.TemplateName}) {
		t.Fatalf("GetInfo().SupportedTemplates=%v; want %v", i.SupportedTemplates, []string{metric.TemplateName})
	}
}

func TestBuild(t *testing.T) {
	s := server{}
}