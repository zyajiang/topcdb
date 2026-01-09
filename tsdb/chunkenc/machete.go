package chunkenc

/*
#cgo CFLAGS: -I./algorithm
#cgo LDFLAGS: -L./algorithm/lib -lmachC -lmach
#include "Machete/macheteC/wrapper.h"
*/
import "C"
import "unsafe"

func Machete_Compress(data []float64, len int64, absErrBound float64) []byte {
	var outSize int64
	compressed_data := C.machete_compress_C((*C.double)(&data[0]), C.long(len), C.double(absErrBound), (*C.long)(&outSize))
	return C.GoBytes(unsafe.Pointer(compressed_data), C.int(outSize))
	// return nil
}

func Machete_Decompress(bytes []byte, bytelength int64, esize int64) []float64 {
	var outSize int64
	decompressed_data := C.machete_decompress_C((*C.uchar)(&bytes[0]), C.long(esize), C.long(bytelength), (*C.long)(&outSize))
	data_ptr := unsafe.Pointer(decompressed_data)
	arr := make([]float64, 0, esize)
	for i := 0; i < int(esize); i += 1 {
		// Convert to float64
		arr = append(arr, *(*float64)(unsafe.Pointer(uintptr(data_ptr) + uintptr(i)*unsafe.Sizeof(C.double(0)))))
	}
	return arr
	// return nil
}
