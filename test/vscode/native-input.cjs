exports.nativeInput = (ui) => {
  const visible = ".quick-input-widget:not([style*='display: none'])";
  const field = `document.querySelector(${JSON.stringify(`${visible} input`)})`;
  const title = (text) =>
    `document.querySelector(${JSON.stringify(visible)})?.offsetParent!=null && document.querySelector(${JSON.stringify(`${visible} .quick-input-title`)})?.textContent.includes(${JSON.stringify(text)})`;
  const input = async (text, value) => {
    await ui.until(title(text));
    await ui.evaluate(
      `(()=>{const input=${field};input.focus();input.value=${JSON.stringify(value)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.until(`document.activeElement===(${field})`);
    await ui.key("Enter");
  };
  const focus = async (many = false) => {
    // Focusing is not a click action. A coordinate sampled while the picker
    // moves can hit the underlying editor and dismiss the pending question.
    await ui.evaluate(`(${field}).focus()`);
    await ui.until(
      `document.activeElement===(${field}) && ${many ? `document.querySelectorAll(${JSON.stringify(`${visible} .quick-input-list .monaco-list-row`)}).length>0` : `!!document.querySelector(${JSON.stringify(`${visible} .quick-input-list .monaco-list-row.focused`)})`}`,
    );
  };
  const first = async (text, many = false) => {
    await ui.until(title(text));
    await focus(many);
    await ui.key("Enter");
  };
  const pick = async (text, label) => {
    await ui.until(title(text));
    await ui.evaluate(
      `(()=>{const input=${field};input.value=${JSON.stringify(label)};input.dispatchEvent(new Event('input',{bubbles:true}));})()`,
    );
    await ui.until(
      `[...document.querySelectorAll(${JSON.stringify(`${visible} .quick-input-list .monaco-list-row`)})].some(row=>row.textContent.includes(${JSON.stringify(label)}))`,
    );
    await focus();
    await ui.until(
      `document.querySelector(${JSON.stringify(`${visible} .quick-input-list .monaco-list-row.focused`)})?.textContent.includes(${JSON.stringify(label)})`,
    );
    await ui.key("Enter");
  };
  const consent = async (text) => {
    const button = `[...document.querySelectorAll('.monaco-dialog-box .monaco-button')].find(button=>button.textContent.trim()===${JSON.stringify(text)})`;
    await ui.until(`!!(${button})`);
    await ui.click(button);
  };
  return { visible, field, title, input, first, pick, consent };
};
