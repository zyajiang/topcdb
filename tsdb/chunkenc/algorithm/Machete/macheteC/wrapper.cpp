#include "../machete/machete.h"
#include "wrapper.h"
#include "stdio.h"
#include "stdlib.h"

unsigned char* machete_compress_C(double* input, long len, double error, long* outSize) {
    unsigned char* output;
    *outSize = machete_compress<lorenzo1,huffman>(input, len, &output, error);
    return output;
}

double* machete_decompress_C(unsigned char* input, long esize, long size, long* outSize) {
    double* output = (double*) malloc(esize * sizeof(double));
    *outSize = machete_decompress<lorenzo1,huffman>(input, size, output);
    return output;
}

// int main() {
//     double input[1000];
//     for (int i = 0; i < 1000; ++i) {
//         input[i] = i * 1.111;
//     }
//     double buffer[2000];
//     long outSize;
//     unsigned char* output = machete_compress_C(input, 1000, 0.5, &outSize);
//     printf("outSize: %ld\n", outSize);
//     long inputSize;
//     machete_decompress_C(output, buffer, outSize, &inputSize);
//     printf("inputSize: %ld\n", inputSize);
//     for (int i = 0; i < 1000; ++i) {
//         if (buffer[i] - input[i] > 0.5 || buffer[i] - input[i] < -0.5) {
//             printf("ERROR: data[%d]: %f (%f)\n", i, buffer[i], input[i]);
//         }
//     }
// }