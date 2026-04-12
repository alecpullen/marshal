; Definitions
(function_declaration
  name: (identifier) @name.definition.function) @definition.function

(method_declaration
  name: (field_identifier) @name.definition.method) @definition.method

(type_declaration
  (type_spec
    name: (type_identifier) @name.definition.type)) @definition.type

(const_declaration
  (const_spec
    name: (identifier) @name.definition.const)) @definition.const

(var_declaration
  (var_spec
    name: (identifier) @name.definition.var)) @definition.var

; References — function/method calls
(call_expression
  function: (identifier) @name.reference.call) @reference.call

(call_expression
  function: (selector_expression
    field: (field_identifier) @name.reference.call)) @reference.call

; References — type uses
(composite_literal
  type: (type_identifier) @name.reference.type) @reference.type

(type_assertion_expression
  type: (type_identifier) @name.reference.type) @reference.type
