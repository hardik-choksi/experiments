package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "/home/hardik/.kube/config", "kubeconfig file")
	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		fmt.Printf("error reading config from kubeconfig file path: %s\n", err)
		fmt.Printf("trying in-cluster methods...\n")
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err)
		}
	}
	fmt.Printf("auth sucessfull!")
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err)
	}

	ticker := time.NewTicker(1 * time.Second)
	done := make(chan struct{})
	ctx := context.Background()

	go func(ctx context.Context) {
		for t := range ticker.C {
			fmt.Println(">>> ticker at ", t)

			nodes, err := clientset.CoreV1().Nodes().List(ctx, v1.ListOptions{})
			if err != nil {
				fmt.Printf("error getting node list: %s\n", err)
			}
			fmt.Println("---nodes---")
			for _, node := range nodes.Items {
				fmt.Printf(">> %s\n", node.Name)
			}

			nss, err := clientset.CoreV1().Namespaces().List(ctx, v1.ListOptions{})
			if err != nil {
				fmt.Printf("error getting ns list: %s\n", err)
			}
			fmt.Println("---namespaces---")
			for _, ns := range nss.Items {
				fmt.Printf(">> %s\n", ns.Name)
			}
			fmt.Println("---pods---")
			pods, err := clientset.CoreV1().Pods("").List(ctx, v1.ListOptions{})
			if err != nil {
				fmt.Printf("error getting pod list: %s\n", err)
			}
			for _, pod := range pods.Items {
				fmt.Printf(">> %s\n", pod.Name)
			}
		}
	}(ctx)

	<-done
}
