import type { ReactNode } from "react";

import { cn } from "@/lib/utils";
import { Select, SelectContent, SelectGroup, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

export type SelectOption = {
  value: string;
  label: ReactNode;
  disabled?: boolean;
};

type SelectControlProps = {
  "aria-invalid"?: boolean;
  "aria-label"?: string;
  className?: string;
  disabled?: boolean;
  id?: string;
  onValueChange: (value: string) => void;
  options: readonly SelectOption[];
  placeholder?: ReactNode;
  required?: boolean;
  size?: "sm" | "default";
  value: string;
};

export function SelectControl({ className, disabled, id, onValueChange, options, placeholder, required, size, value, ...accessibility }: SelectControlProps) {
  return (
    <Select disabled={disabled} id={id} items={options} onValueChange={(next) => { if (next !== null) onValueChange(next); }} required={required} value={value}>
      <SelectTrigger className={cn("w-full", className)} size={size} {...accessibility}>
        <SelectValue placeholder={placeholder} />
      </SelectTrigger>
      <SelectContent>
        <SelectGroup>
          {options.map((option) => <SelectItem disabled={option.disabled} key={option.value} value={option.value}>{option.label}</SelectItem>)}
        </SelectGroup>
      </SelectContent>
    </Select>
  );
}
