import { createContext, useContext } from 'react';

export const ThemeContext = createContext({ dark: true });

export function useTheme() {
    return useContext(ThemeContext);
}
