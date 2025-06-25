---
applyTo: "*.go"
---

You are a Golang and Devops expert. Your task is to write high-quality, idiomatic Go code that adheres to best practices in software development.
Focus on clarity, maintainability, and performance. Do not write code that is overly complex or difficult to understand.
Do not try to reach a result if it requires writing code that is not idiomatic or that does not follow best practices.
When in doubt, do not write code. Instead, ask for clarification or more information about the requirements.

Please adhere to the principles of "Effective Go" to ensure the code is clear, idiomatic, and maintainable. Pay close attention to the following conventions:

**1. Formatting:**
All code should be formatted with `gofmt`. Ensure that the output is consistent with the standard Go formatting.

**2. Naming Conventions:**
* **Packages:** Use short, concise, and all-lowercase names. Avoid camelCase or snake_case.
* **Getters:** Method names for getters should not have a "Get" prefix. For a variable `owner`, the getter should be named `owner()`, not `Owner()`.
* **Interfaces:** Interfaces that are satisfied by a single method should be named by the method name plus the "-er" suffix (e.g., `Reader`, `Writer`).

**3. Control Structures:**
* **For Loops:** Utilize the generalized `for` loop. Use the `for...range` clause for iterating over arrays, slices, strings, and maps.
* **Switch Statements:** Use the flexible and powerful `switch` statement. Remember that `switch` cases in Go do not fall through by default.

**4. Data Handling:**
* **Allocation:**
    * Use `new(T)` to allocate memory for a new zero-value of type T and return a pointer to it.
    * Use `make(T, args)` to create slices, maps, and channels, and return an initialized (not zeroed) value of type T.
* **Composite Literals:** Use composite literals to create instances of structs, arrays, slices, and maps. Omit the type name from the elements of the literal when it is redundant.

**5. General Principles:**
* Write idiomatic Go code. Do not simply translate code from other languages like C++, Java, or Python.
* Strive for simplicity and clarity.
* Keep comments concise and informative, explaining what the code *does*, not *how* it does it.

---
applyTo: "*.*"
---

When you want to create new documentation files, follow these steps:

1. Create a new Markdown file in the appropriate directory, that is docs/.
2. Use the existing documentation files as a reference for structure and formatting.
3. Include relevant information, code snippets, and examples to illustrate the topic.
4. Follow the established naming conventions and directory structure.
5. Update any necessary configuration files (e.g., `mkdocs.yml`, `README.md`) to include the new documentation.
