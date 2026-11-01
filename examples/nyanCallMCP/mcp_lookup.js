// Register this exact script path in allowedScripts. Uses a reviewed read tool.
(function () {
  const result = nyanCallMCP({connection: "inventory-read", tool: "lookup_stock", arguments: {sku: "CAT-001"}, options: {timeoutMs: 3000}});
  if (result.resultType === "input_required" || result.isError === true) {
    throw new Error("Inventory lookup did not complete");
  }
  if (!Object.prototype.hasOwnProperty.call(result, "structuredContent")) {
    throw new Error("Expected structuredContent");
  }
  // Return JSON text for compatibility with the product's existing response path.
  return JSON.stringify({status: 200, result: result.structuredContent});
})();
