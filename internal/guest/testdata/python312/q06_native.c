#define PY_SSIZE_T_CLEAN
#include <Python.h>

// Q06 目标 ABI 扩展:两个整数相加,用于验证客体 CPython 的 dlopen、
// 符号解析与模块初始化路径。
static PyObject *q06_add(PyObject *self, PyObject *args) {
    long left, right;
    if (!PyArg_ParseTuple(args, "ll", &left, &right)) {
        return NULL;
    }
    return PyLong_FromLong(left + right);
}

static PyMethodDef q06_methods[] = {
    {"add", q06_add, METH_VARARGS, "return the sum of two integers"},
    {NULL, NULL, 0, NULL},
};

static struct PyModuleDef q06_module = {
    PyModuleDef_HEAD_INIT,
    "q06_native",
    NULL,
    -1,
    q06_methods,
};

PyMODINIT_FUNC PyInit_q06_native(void) {
    return PyModule_Create(&q06_module);
}
