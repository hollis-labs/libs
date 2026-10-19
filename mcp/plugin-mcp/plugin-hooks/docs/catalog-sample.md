# Hook catalog 1

Declarations and policy only. Discovery grants no registration authority.

## assistant.response

    {
      "name": "assistant.response",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## context.window

    {
      "name": "context.window",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "array"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "array"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## envelope.data

    {
      "name": "envelope.data",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "object"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "object"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## reflex.action

    {
      "name": "reflex.action",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "object"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "object"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## reflex.state

    {
      "name": "reflex.state",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "object"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "object"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## system.prompt

    {
      "name": "system.prompt",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## tool.result

    {
      "name": "tool.result",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## tool.selection

    {
      "name": "tool.selection",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "array"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "array"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }

## user.message

    {
      "name": "user.message",
      "kind": "filter",
      "mode": "waterfall",
      "input_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "output_schema": {
        "$schema": "https://json-schema.org/draft/2020-12/schema",
        "type": "object",
        "required": [
          "value"
        ],
        "additionalProperties": false,
        "properties": {
          "value": {
            "type": "string"
          }
        }
      },
      "mutable_paths": [
        "/value"
      ],
      "since": "proposed",
      "remote_ok": false,
      "budget_ms": 5000,
      "handler_timeout_ms": 1000,
      "on_error_default": "open",
      "allowed_on_error": [
        "open"
      ],
      "max_payload_bytes": 1048576,
      "max_handlers": 64,
      "max_parallelism": 1,
      "views": {},
      "schema_digest": "illustrative-not-for-registration"
    }
