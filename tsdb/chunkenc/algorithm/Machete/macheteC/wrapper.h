#ifdef __cplusplus
extern "C" {
#endif

unsigned char* machete_compress_C(double* input, long len, double error, long* outSize);

double* machete_decompress_C(unsigned char* input, long esize, long size, long* outSize);

#ifdef __cplusplus
}
#endif