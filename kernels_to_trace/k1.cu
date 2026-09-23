#include<iostream>


using namespace std;




__global__ void multiply_scalar(int scalar, int *array, int size) {


    int idx = blockIdx.x * blockDim.x + threadIdx.x;

    if(idx < size) {
        array[idx] *= scalar;
    }
}



int main(){

    int arr[100];
    for(int i=0; i<100; i++){
        arr[i] = i;
    }

    multiply_scalar<<<1, 100>>>(2, arr, 100);



    return 0;
}