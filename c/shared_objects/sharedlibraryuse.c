# include <stdio.h>
# include <math.h>
# include <stdlib.h>
# include <time.h>

int main(void) {
  time_t t;
  srand((unsigned) time(&t));
  int rNum = rand();
  double sr = sqrt(rNum);
  printf("square root of %d is %f\n", rNum, sr);
  return 0;
}
