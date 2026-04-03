#include <bits/stdc++.h>

typedef struct {
	std::string name;
	int id;
} Task;

int main() {
	Task t = Task{"Todo", 1};	
	Task* ptr = &t;
	printf("id:%d\n", ptr->id);
	return 0;
}
