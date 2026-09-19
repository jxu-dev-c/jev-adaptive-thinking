// C ABI bridge follows the MIT-licensed CLIProxyAPI Go plugin examples.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

var runtime = newPluginRuntime()

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || uint32(host.abi_version) != pluginabi.ABIVersion {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (code C.int) {
	if response == nil {
		return 1
	}
	response.ptr, response.len = nil, 0
	defer func() {
		if recover() != nil {
			if response.ptr != nil {
				C.free(response.ptr)
			}
			raw, _ := pluginabi.NewErrorEnvelope("plugin_error", "plugin call failed")
			response.ptr, response.len = C.CBytes(raw), C.size_t(len(raw))
			code = 1
		}
	}()
	var raw []byte
	if method == nil || uint64(requestLen) > 2147483647 || (request == nil && requestLen != 0) {
		raw, _ = pluginabi.NewErrorEnvelope("invalid_request", "invalid ABI request")
		code = 1
	} else {
		var data []byte
		if requestLen > 0 {
			data = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
		}
		result, err := runtime.handle(C.GoString(method), data)
		if err != nil {
			raw, _ = pluginabi.NewErrorEnvelope("plugin_error", err.Error())
			code = 1
		} else {
			payload, err := json.Marshal(result)
			if err != nil {
				raw, _ = pluginabi.NewErrorEnvelope("plugin_error", "cannot encode plugin response")
				code = 1
			} else {
				raw, _ = json.Marshal(pluginabi.Envelope{OK: true, Result: payload})
			}
		}
	}
	response.ptr, response.len = C.CBytes(raw), C.size_t(len(raw))
	return code
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	runtime.shutdown()
}
