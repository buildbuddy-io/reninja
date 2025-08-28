// Copyright 2024 The Gin Authors. All Rights Reserved.
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

package remote

import "strings"

// parseEndpoint parses the endpoint and determines if TLS should be used
func parseEndpoint(endpoint string) (string, bool) {
	if endpoint == "" {
		return "", false
	}
	
	// If no scheme specified, default to grpcs://
	if !strings.Contains(endpoint, "://") {
		endpoint = "grpcs://" + endpoint
	}
	
	// Parse the endpoint
	if strings.HasPrefix(endpoint, "grpc://") {
		// Insecure gRPC - default port 80
		host := strings.TrimPrefix(endpoint, "grpc://")
		if !strings.Contains(host, ":") {
			host += ":80"
		}
		return host, false
	} else if strings.HasPrefix(endpoint, "grpcs://") {
		// Secure gRPC - default port 443
		host := strings.TrimPrefix(endpoint, "grpcs://")
		if !strings.Contains(host, ":") {
			host += ":443"
		}
		return host, true
	}
	
	// Unknown scheme, assume secure and return as-is
	return endpoint, true
}

// detectOSFamily returns the OS family for platform properties
// For remote execution, we should use the target platform, not local
func detectOSFamily() string {
	// BuildBuddy and most remote execution services support Linux executors
	// We default to Linux for remote execution compatibility
	return "Linux"
}

// detectArch returns the architecture for platform properties
func detectArch() string {
	// Most remote execution services use amd64
	return "amd64"
}