package chunkenc

/*
#cgo CFLAGS: -I./algorithm
#cgo LDFLAGS: -L./algorithm/lib -lSZ3c -fopenmp -lm -lstdc++ -lzstd
#include "SZ3/tools/sz3c/include/sz3c.h"
#include <stdlib.h>
*/
import "C"
import (
	"unsafe"
)

// func SZ_Compress(input []float64, input_len int64, output []byte, absErrBound float64) {
// 	C.SZ_compress((*C.double)(&input[0]), C.size_t(input_len), (**C.uchar)(&(&output[0])), C.double(absErrBound))
// }

func SZ_Compress(datatype int, data []float64, outSize *uint64, errBoundMode int, absErrBound float64, relBoundRatio float64,
	pwdBoundRatio float64, r5, r4, r3, r2, r1 uint64) []byte {
	compressed_data := C.SZ_compress_args(C.int(datatype), (unsafe.Pointer)(&data[0]), (*C.size_t)(outSize), C.int(errBoundMode),
		C.double(absErrBound), C.double(relBoundRatio), C.double(pwdBoundRatio),
		C.size_t(r5), C.size_t(r4), C.size_t(r3), C.size_t(r2), C.size_t(r1))
	defer C.free(unsafe.Pointer(compressed_data))
	return C.GoBytes(unsafe.Pointer(compressed_data), C.int(*outSize))
	// return nil
}

// func SZ_Decompress(input []byte, input_len int64, output []float64, absErrBound float64) {
// 	C.SZ_decompress((unsafe.Pointer)(&input[0]), C.size_t(input_len), (unsafe.Pointer)(&output), C.double(absErrBound))
// }

func SZ_Decompress(datatype int, bytes []byte, bytelength uint64, r5, r4, r3, r2, r1 uint64) []float64 {
	decompressed_data := C.SZ_decompress(C.int(datatype), (*C.uchar)((unsafe.Pointer)(&bytes[0])), C.size_t(bytelength),
		C.size_t(r5), C.size_t(r4), C.size_t(r3), C.size_t(r2), C.size_t(r1))
	arr := make([]float64, 0, r1)
	for i := 0; i < int(r1); i += 1 {
		// Convert to float64
		arr = append(arr, *(*float64)(unsafe.Pointer(uintptr(decompressed_data) + uintptr(i)*unsafe.Sizeof(C.double(0)))))
	}
	defer C.free(decompressed_data)
	return arr
	// return nil
}
