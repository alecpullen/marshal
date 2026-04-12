; Definitions
(function_declaration
  name: (identifier) @name.definition.function) @definition.function

(method_definition
  name: (property_identifier) @name.definition.method) @definition.method

(class_declaration
  name: (identifier) @name.definition.class) @definition.class

(variable_declarator
  name: (identifier) @name.definition.var
  value: [(arrow_function) (function_expression)]) @definition.function

; References
(call_expression
  function: (identifier) @name.reference.call) @reference.call

(call_expression
  function: (member_expression
    property: (property_identifier) @name.reference.call)) @reference.call

(new_expression
  constructor: (identifier) @name.reference.class) @reference.class
