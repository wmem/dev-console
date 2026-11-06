export default {
  projectId: 'dev-console',
  title: 'Dev Console 使用与开发文档',
  documents: ['docs/index.mdoc', 'docs/usage', 'docs/development'],
  views: [
    {
      "id": "device:visual:services",
      "kind": "architecture",
      "label": "项目服务如何连接设备能力",
      "include": {
        "objects": [
          "device:visual:services:caller",
          "device:visual:services:project",
          "device:visual:services:device",
          "device:visual:services:access",
          "device:visual:services:instrument"
        ],
        "relations": [
          "device:visual:services:caller-to-project",
          "device:visual:services:project-to-device",
          "device:visual:services:device-to-access",
          "device:visual:services:access-to-instrument"
        ]
      },
      "layout": {
        "direction": "TB",
        "routing": "orthogonal",
        "constraints": [
          {
            "type": "align",
            "axis": "y",
            "nodes": [
              "device:visual:services:caller",
              "device:visual:services:project",
              "device:visual:services:device"
            ]
          },
          {
            "type": "align",
            "axis": "y",
            "nodes": [
              "device:visual:services:instrument",
              "device:visual:services:access"
            ]
          },
          {
            "type": "order",
            "axis": "x",
            "nodes": [
              "device:visual:services:caller",
              "device:visual:services:project",
              "device:visual:services:device"
            ],
            "gap": 80
          },
          {
            "type": "order",
            "axis": "x",
            "nodes": [
              "device:visual:services:instrument",
              "device:visual:services:access"
            ],
            "gap": 80
          },
          {
            "type": "order",
            "axis": "y",
            "nodes": [
              "device:visual:services:device",
              "device:visual:services:access"
            ],
            "gap": 100
          },
          {
            "type": "align",
            "axis": "x",
            "nodes": [
              "device:visual:services:device",
              "device:visual:services:access"
            ]
          },
          {
            "type": "align",
            "axis": "x",
            "nodes": [
              "device:visual:services:project",
              "device:visual:services:instrument"
            ]
          }
        ]
      },
      "source": {
        "file": "docs/usage/overview.mdoc",
        "selector": "device:usage:overview"
      }
    },
    {
      "id": "device:visual:invocation",
      "kind": "architecture",
      "label": "一次调用的正常执行路径",
      "include": {
        "objects": [
          "device:visual:invocation:auth",
          "device:visual:invocation:parameters",
          "device:visual:invocation:busy",
          "device:visual:invocation:audit",
          "device:visual:invocation:io",
          "device:visual:invocation:finish"
        ],
        "relations": [
          "device:visual:invocation:auth-to-parameters",
          "device:visual:invocation:parameters-to-busy",
          "device:visual:invocation:busy-to-audit",
          "device:visual:invocation:audit-to-io",
          "device:visual:invocation:io-to-finish"
        ]
      },
      "layout": {
        "direction": "TB",
        "routing": "orthogonal",
        "constraints": [
          {
            "type": "align",
            "axis": "y",
            "nodes": [
              "device:visual:invocation:auth",
              "device:visual:invocation:parameters",
              "device:visual:invocation:busy"
            ]
          },
          {
            "type": "align",
            "axis": "y",
            "nodes": [
              "device:visual:invocation:finish",
              "device:visual:invocation:io",
              "device:visual:invocation:audit"
            ]
          },
          {
            "type": "order",
            "axis": "x",
            "nodes": [
              "device:visual:invocation:auth",
              "device:visual:invocation:parameters",
              "device:visual:invocation:busy"
            ],
            "gap": 80
          },
          {
            "type": "order",
            "axis": "x",
            "nodes": [
              "device:visual:invocation:finish",
              "device:visual:invocation:io",
              "device:visual:invocation:audit"
            ],
            "gap": 80
          },
          {
            "type": "order",
            "axis": "y",
            "nodes": [
              "device:visual:invocation:busy",
              "device:visual:invocation:audit"
            ],
            "gap": 100
          },
          {
            "type": "align",
            "axis": "x",
            "nodes": [
              "device:visual:invocation:auth",
              "device:visual:invocation:finish"
            ]
          },
          {
            "type": "align",
            "axis": "x",
            "nodes": [
              "device:visual:invocation:parameters",
              "device:visual:invocation:io"
            ]
          },
          {
            "type": "align",
            "axis": "x",
            "nodes": [
              "device:visual:invocation:busy",
              "device:visual:invocation:audit"
            ]
          }
        ]
      },
      "source": {
        "file": "docs/usage/integration/invoking.mdoc",
        "selector": "device:usage:integration:invoking"
      }
    }
  ],
  output: { html: 'output/index.html' },
};
