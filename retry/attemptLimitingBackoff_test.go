// Copyright 2022 LINE Corporation
//
// LINE Corporation licenses this file to you under the Apache License,
// version 2.0 (the "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at:
//
//   https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package retry

import "testing"

func TestAttemptLimitingBackoff(t *testing.T) {
	if _, err := NewAttemptLimitingBackoff(nil, 1); err == nil {
		t.FailNow()
	}

	fixedBackoff, _ := NewFixedBackoff(123)
	if _, err := NewAttemptLimitingBackoff(fixedBackoff, 0); err == nil {
		t.FailNow()
	}

	b, err := NewAttemptLimitingBackoff(fixedBackoff, 3)
	if err != nil || b == nil {
		t.FailNow()
	}

	for i := 0; i < 5; i++ {
		if next := b.NextDelayMillis(i); !(i < 3 && next == 123) && !(i >= 3 && next == -1) {
			t.FailNow()
		}
	}
}
