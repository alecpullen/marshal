; Definitions
(function_definition
  name: (identifier) @name.definition.function) @definition.function

(class_definition
  name: (identifier) @name.definition.class) @definition.class

; References
(call
  function: (identifier) @name.reference.call) @reference.call

(call
  function: (attribute
    attribute: (identifier) @name.reference.call)) @reference.call
