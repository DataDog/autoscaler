#!/usr/bin/env python3

import argparse
from kubernetes import client, config
import math
import random
import re
import requests
import sys
import time


def parse_arguments():
    parser = argparse.ArgumentParser(description='')
    parser.add_argument('--dest', type=str, default='pushservice')
    parser.add_argument('--mean_cpu', type=int, default='1000', help='Mean millicores for cpu.')
    parser.add_argument('--mean_mem', type=int, default='128', help='Mean megabytes for memory.')
    parser.add_argument('--stddev_cpu', type=int, default=150, help='Standard deviation for cpu.')
    parser.add_argument('--stddev_mem', type=int, default=15, help='Standard deviation for mem.')
    parser.add_argument('--sleep_sec', type=int, default=30, help='Delay between metric-sends, in seconds.')
    parser.add_argument('-t','--tags', action='append', nargs=2, metavar=('key','value'), default=[['app', 'prometheus']],
                        help='Additional tags to attach to metrics.')
    parser.add_argument('--container', default='prometheus', help='container name for metrics.')
    parser.add_argument('--namespace_pattern', default='monitoring', help='Regex to match namespace names.')
    parser.add_argument('--pod_pattern', default='prometheus-[0-9a-f]{9}-[0-9a-z]{5}', help='Regex to match pod names.')
    parser.add_argument('--job', default='emit-metrics', help='Job name to submit under.')
    return parser.parse_args()


def send_metrics(args, path, cpuval, memval):
    payload = f"cpu {cpuval:d}.0\nmem {memval:d}.0\n"
    path_str = '/'.join([f"{key}/{value}" for key, value in path.items()])
    url = f'http://{args.dest}/metrics/job/{args.job}/namespace/{path["namespace"]}/{path_str}'
    response = requests.put(url=url, data=bytes(payload, 'utf-8'))
    if response.status_code != 200:
        print (f"Writing to {url} and got {response.status_code}: {response.reason}, {response.text}")
    else:
        print (f"Wrote to {url}")
    sys.stdout.flush()



def main(args):
    print (f"Starting up.")
    sys.stdout.flush()
    pod_name_pattern = re.compile(args.pod_pattern)
    namespace_name_pattern = re.compile(args.namespace_pattern)
    try:
        config.load_kube_config()
    except:
        config.load_incluster_config()
    v1 = client.CoreV1Api()
    print (f"Initialized.  Sleep interval is for {args.sleep_sec} seconds.")
    sys.stdout.flush()
    while True:
        time.sleep(args.sleep_sec)
        ret = v1.list_pod_for_all_namespaces(watch=False)
        all = 0
        found = 0
        for pod in ret.items:
            all += 1
            if namespace_name_pattern.match(pod.metadata.namespace) and pod_name_pattern.match(pod.metadata.name):
                found += 1
                cpuval = random.normalvariate(args.mean_cpu, args.stddev_cpu)
                memval = random.normalvariate(args.mean_mem, args.stddev_mem)
                path = { "kubernetes_namespace": pod.metadata.namespace,  "name": args.container,
                         "kubernetes_pod_name": pod.metadata.name, "container": args.container,
                         "pod": pod.metadata.name,
                         "namespace": pod.metadata.namespace}
                for k,v in args.tags:
                    path[k] = v
                send_metrics(args, path, math.floor(cpuval), math.floor(memval * 1048576.0))
        print(f"Found {found} out of {all} pods.")

if __name__ == '__main__':
    main(parse_arguments())
