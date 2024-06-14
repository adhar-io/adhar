bundle: {
    apiVersion: "v1alpha1"
    name:       "adhar"
    instances: {
        "adhar-console": {
            module: url: "file://../modules/adhar-console"
            namespace: "adhar-system"
            values: {
                host:    "example.com" @timoni(runtime:string:MY_HOST)
                enabled: true          @timoni(runtime:bool:MY_ENABLED)
                score:   1             @timoni(runtime:number:MY_SCORE)
            }
        }
        "adhar-backstage": {
            module: url: "file://../modules/adhar-backstage"
            namespace: "adhar-system"
            values: {
                host:    "example.com" @timoni(runtime:string:MY_HOST)
                enabled: true          @timoni(runtime:bool:MY_ENABLED)
                score:   1             @timoni(runtime:number:MY_SCORE)
            }
        }
    }
}