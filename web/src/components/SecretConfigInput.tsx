import { useState } from "react";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Textarea } from "./ui/textarea";
import type { Language } from "../translations";

export function SecretConfigInput({ id, value, onChange, required, placeholder, language }: {
  id: string; value: string; onChange: (value: string) => void;
  required: boolean; placeholder?: string; language: Language;
}) {
  const [visible, setVisible] = useState(false);
  const multiline = /[\r\n]/.test(value);
  const chinese = language === "zh-CN";
  return <>
    {visible ? <Textarea id={id} value={value} required={required} placeholder={placeholder}
      autoComplete="off" spellCheck={false} autoCapitalize="off" rows={5}
      onChange={(event) => onChange(event.target.value)} /> : <Input
      id={id} type="password" autoComplete="new-password" required={required}
      placeholder={placeholder} value={multiline ? "••••••••" : value} readOnly={multiline}
      onChange={(event) => onChange(event.target.value)}
      onPaste={(event) => {
        const pasted = event.clipboardData.getData("text/plain");
        if (/[\r\n]/.test(pasted)) {
          event.preventDefault();
          onChange(pasted);
        }
      }} />}
    <Button type="button" size="sm" variant="outline" aria-controls={id} aria-pressed={visible}
      onClick={() => setVisible((current) => !current)}>
      {visible ? (chinese ? "隐藏内容" : "Hide content") : (chinese ? "多行编辑（显示内容）" : "Edit multiline (show content)")}
    </Button>
  </>;
}
