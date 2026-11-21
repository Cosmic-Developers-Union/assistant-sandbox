/* Q23:最小 N-API 扩展,验证目标架构原生扩展装载与调用。 */
#include <node_api.h>

static napi_value Add(napi_env env, napi_callback_info info) {
	size_t argc = 2;
	napi_value argv[2];
	napi_value result;
	double left = 0;
	double right = 0;
	if (napi_get_cb_info(env, info, &argc, argv, NULL, NULL) != napi_ok) return NULL;
	if (argc < 2) return NULL;
	if (napi_get_value_double(env, argv[0], &left) != napi_ok) return NULL;
	if (napi_get_value_double(env, argv[1], &right) != napi_ok) return NULL;
	if (napi_create_double(env, left + right, &result) != napi_ok) return NULL;
	return result;
}

NAPI_MODULE_INIT() {
	napi_value add;
	if (napi_create_function(env, "add", NAPI_AUTO_LENGTH, Add, NULL, &add) != napi_ok) return NULL;
	if (napi_set_named_property(env, exports, "add", add) != napi_ok) return NULL;
	return exports;
}
